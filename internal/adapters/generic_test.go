// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/inputs"
)

func model(t *testing.T, text string) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFileSources(t *testing.T) {
	dir := t.TempDir()
	csv := "sku,on_hand,class,amount,profit\nA,0,fast,\"1,000\",250\nB,5,fast,500,100\nC,0,slow,250,50\nD,9,slow,250,100\n"
	if err := os.WriteFile(filepath.Join(dir, "stock.csv"), []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "m.yaml"), []byte("meters:\n  - kwh: 10\n  - kwh: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := model(t, `
name: t
kpis:
  - {id: stockout, value: 0, source: {kind: file, file: stock.csv, field: "#(on_hand=0)", denominator: "#"}}
  - {id: fast_out, value: 0, source: {kind: file, file: stock.csv, field: "#(on_hand=0)", where: {class: fast}}}
  - {id: margin, value: 0, source: {kind: file, file: stock.csv, field: "*.profit", denominator: "*.amount"}}
  - {id: max_amount, value: 0, source: {kind: file, file: stock.csv, field: "*.amount", agg: max}}
  - {id: kwh, value: 0, source: {kind: file, file: m.yaml, field: "meters.*.kwh", agg: avg}}
  - {id: missing, value: 7, source: {kind: file, file: nope.csv, field: "#"}}
edges: []
actions: []
`)
	m.Dir = dir
	cfg := Config{Files: NewFileCache()}
	rep, err := Refresh(context.Background(), m, cfg)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("want error for missing file, got %v", err)
	}
	want := map[string]float64{"stockout": 0.5, "fast_out": 1, "margin": 0.25, "max_amount": 1000, "kwh": 20, "missing": 7}
	for id, v := range want {
		if k, _ := m.KPI(id); k.Value != v {
			t.Errorf("%s = %v, want %v", id, k.Value, v)
		}
	}
	if !rep.KPIs["missing"].Fallback {
		t.Errorf("missing file should leave the KPI on fallback: %+v", rep.KPIs["missing"])
	}

	// A rewritten file is re-read.
	csv2 := "sku,on_hand,class,amount,profit\nA,3,fast,1000,250\nB,5,fast,500,100\n"
	path := filepath.Join(dir, "stock.csv")
	if err := os.WriteFile(path, []byte(csv2), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	_, _ = Refresh(context.Background(), m, cfg)
	if k, _ := m.KPI("stockout"); k.Value != 0 {
		t.Errorf("stockout after reload = %v, want 0", k.Value)
	}
}

func TestHTTPAndSheetSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/sales":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"today":{"total":125000}}`))
		case "/sheet":
			_, _ = w.Write([]byte("day,wait\nmon,3\ntue,5\n"))
		}
	}))
	defer srv.Close()
	t.Setenv("ZYNTRA_TEST_POS", srv.URL)
	t.Setenv("ZYNTRA_TEST_TOKEN", "s3cret")
	t.Setenv("HOME_SECRET", "nope")
	m := model(t, `
name: t
kpis:
  - {id: sales, value: 0, source: {kind: http, url: "${ZYNTRA_TEST_POS}/sales", field: today.total, headers: {Authorization: "Bearer ${ZYNTRA_TEST_TOKEN}"}}}
  - {id: wait, value: 0, source: {kind: sheet, url: "${ZYNTRA_TEST_POS}/sheet", field: "*.wait", agg: avg, headers: {Authorization: "Bearer ${ZYNTRA_TEST_TOKEN}"}}}
  - {id: leak, value: 1, source: {kind: http, url: "${HOME_SECRET}/x", field: a}}
  - {id: unset, value: 2, source: {kind: http, url: "${ZYNTRA_NOT_SET}/x", field: a}}
edges: []
actions: []
`)
	rep, _ := Refresh(context.Background(), m, Config{})
	for id, v := range map[string]float64{"sales": 125000, "wait": 4, "leak": 1, "unset": 2} {
		if k, _ := m.KPI(id); k.Value != v {
			t.Errorf("%s = %v, want %v", id, k.Value, v)
		}
	}
	for _, s := range rep.Sources {
		if strings.Contains(s.Name, "HOME_SECRET") || strings.Contains(s.Name, "ZYNTRA_NOT_SET") {
			if s.State != StateFallback {
				t.Errorf("%s state = %s, want fallback", s.Name, s.State)
			}
		}
		if strings.Contains(s.Error, "s3cret") || strings.Contains(s.Name, "s3cret") {
			t.Errorf("secret leaked into status: %+v", s)
		}
	}
}

func TestWebhookManualAndHold(t *testing.T) {
	store, _ := inputs.Open("")
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	m := model(t, `
name: t
timezone: UTC
calendars:
  shop-hours: {start: "09:00", end: "21:00"}
kpis:
  - {id: upi, value: 0, source: {kind: webhook-in, name: upi, field: amount, staleAfter: 10m}}
  - {id: cashiers, value: 1, source: {kind: manual}}
  - {id: wait, value: 0, calendar: shop-hours, source: {kind: webhook-in, name: queue, field: wait}}
edges: []
actions: []
`)
	clock := now
	cfg := Config{Inputs: store, Holds: NewHoldTracker(), Now: func() time.Time { return clock }}

	rep, _ := Refresh(context.Background(), m, cfg)
	if !rep.KPIs["upi"].Fallback || !rep.KPIs["cashiers"].Fallback {
		t.Fatalf("nothing received yet should be fallback: %+v", rep.KPIs)
	}

	_ = store.Ingest("upi", map[string]any{"amount": 4200.0}, "test", now.Add(-time.Minute))
	_ = store.Ingest("queue", map[string]any{"wait": 6.0}, "test", now)
	_ = store.SetManual("cashiers", inputs.Manual{Value: 2, By: "floor", At: now})
	rep, _ = Refresh(context.Background(), m, cfg)
	for id, v := range map[string]float64{"upi": 4200, "cashiers": 2, "wait": 6} {
		if k, _ := m.KPI(id); k.Value != v {
			t.Errorf("%s = %v, want %v", id, k.Value, v)
		}
	}

	// After shop hours a new reading is held, not used.
	clock = time.Date(2026, 10, 2, 23, 0, 0, 0, time.UTC)
	_ = store.Ingest("queue", map[string]any{"wait": 0.0}, "test", clock)
	rep, _ = Refresh(context.Background(), m, cfg)
	if k, _ := m.KPI("wait"); k.Value != 6 || !rep.KPIs["wait"].Held {
		t.Errorf("wait outside shop hours = %v (held %v), want 6 held", k.Value, rep.KPIs["wait"].Held)
	}
	// The UPI document is now 11h old: stale.
	if !rep.KPIs["upi"].Fallback {
		t.Errorf("old webhook should be stale: %+v", rep.KPIs["upi"])
	}
	var upi Status
	for _, s := range rep.Sources {
		if s.Name == "webhook:upi" {
			upi = s
		}
	}
	if upi.State != StateStale {
		t.Errorf("webhook:upi state = %q, want stale", upi.State)
	}
}
