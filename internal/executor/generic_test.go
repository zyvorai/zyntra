// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

const shop = `
name: shop
kpis:
  - {id: stockout_rate, value: 0.03, target: 0.02, direction: lower, owner: buyer}
edges: []
actions:
  - id: reorder
    title: Raise PO
    adapter: webhook
    effects: [{kpi: stockout_rate, change: -0.4}]
    webhook:
      url: ${ZYNTRA_TEST_ERP}/po
      headers: {Authorization: "Bearer ${ZYNTRA_TEST_ERP_TOKEN}"}
      body: {reason: stockout_gap, gap: "gap:stockout_rate", now: "kpi:stockout_rate"}
  - id: po_file
    title: Write PO
    adapter: file
    effects: [{kpi: stockout_rate, change: -0.4}]
    file:
      path: "po/{{.Date}}-reorder.md"
      content: "# PO\nstockout {{pct (index .KPIs \"stockout_rate\").Value}}\n"
  - id: counter
    title: Open second counter
    adapter: noop
    effects: [{kpi: stockout_rate, change: -0.1}]
`

func shopModel(t *testing.T) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(shop))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWebhook(t *testing.T) {
	var got struct {
		auth, key string
		body      map[string]any
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.auth, got.key = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"po":"PO-1"}`))
	}))
	defer srv.Close()
	m := shopModel(t)
	a, _ := m.Action("reorder")
	r, err := RenderIn(m, *a)
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != graph.KindWebhook || !strings.Contains(r.Display, "POST ${ZYNTRA_TEST_ERP}/po") || !strings.Contains(r.Display, `"severity": 0.5`) {
		t.Fatalf("display:\n%s", r.Display)
	}
	if !strings.Contains(r.Display, "not set here: ZYNTRA_TEST_ERP, ZYNTRA_TEST_ERP_TOKEN") {
		t.Errorf("display should say which variables are unset:\n%s", r.Display)
	}

	dry := (&Executor{}).Execute(context.Background(), r)
	if !dry.OK || dry.Mode != ModeDryRun || got.body != nil {
		t.Fatalf("dry-run must not send: %+v", dry)
	}

	apply := &Executor{Mode: ModeApply}
	if res := apply.Execute(context.Background(), r); res.OK || !strings.Contains(res.Error, "ZYNTRA_TEST_ERP") {
		t.Fatalf("apply without the URL set should fail: %+v", res)
	}
	t.Setenv("ZYNTRA_TEST_ERP", srv.URL)
	t.Setenv("ZYNTRA_TEST_ERP_TOKEN", "tok-123")
	r.Key = "p-42"
	res := apply.Execute(context.Background(), r)
	if !res.OK || res.Status != 201 || len(res.ResponseHash) != 64 {
		t.Fatalf("apply: %+v", res)
	}
	if strings.Contains(res.Output, "tok-123") || strings.Contains(r.Display, "tok-123") {
		t.Error("token leaked into output or display")
	}
	if got.auth != "Bearer tok-123" || got.key != "p-42" || got.body["reason"] != "stockout_gap" || got.body["now"] != 0.03 {
		t.Fatalf("received %+v", got)
	}
	if g, _ := got.body["gap"].(map[string]any); g["kpi"] != "stockout_rate" || g["owner"] != "buyer" {
		t.Fatalf("gap summary = %+v", got.body["gap"])
	}
}

func TestFileAndNoop(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 2, 18, 30, 0, 0, time.UTC) }
	defer func() { now = time.Now }()
	m := shopModel(t)
	a, _ := m.Action("po_file")
	r, err := RenderIn(m, *a)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "po/2026-10-02-reorder.md" || !strings.Contains(r.Content, "stockout 3.0%") {
		t.Fatalf("rendered %+v", r)
	}
	dir := t.TempDir()
	ex := &Executor{OutDir: dir}
	if res := ex.Execute(context.Background(), r); !res.OK || res.Written != "" {
		t.Fatalf("dry-run wrote: %+v", res)
	}
	ex.Mode = ModeApply
	res := ex.Execute(context.Background(), r)
	if !res.OK {
		t.Fatal(res.Error)
	}
	b, err := os.ReadFile(filepath.Join(dir, "po/2026-10-02-reorder.md"))
	if err != nil || !strings.Contains(string(b), "# PO") {
		t.Fatalf("file: %q %v", b, err)
	}
	if again := ex.Execute(context.Background(), r); again.OK {
		t.Error("an existing file must not be overwritten")
	}

	esc := graph.Action{ID: "escape", File: &graph.FileOut{Path: "{{.Date}}/../../etc/x", Content: "x"}}
	if _, err := RenderIn(m, esc); err == nil {
		t.Error("a path that escapes the output directory must not render")
	}
	if res := ex.Execute(context.Background(), Rendered{Kind: graph.KindFile, Path: "../x"}); res.OK {
		t.Error("executing an escaping path must fail")
	}

	n, _ := m.Action("counter")
	r, err = RenderIn(m, *n)
	if err != nil || r.Kind != graph.KindNoop || !strings.Contains(r.Display, "no system call") {
		t.Fatalf("noop render %+v %v", r, err)
	}
	if res := ex.Execute(context.Background(), r); !res.OK || res.Kind != graph.KindNoop {
		t.Fatalf("noop: %+v", res)
	}
}
