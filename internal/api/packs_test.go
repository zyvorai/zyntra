// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"context"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/inputs"
)

const counter = `
name: counter
calendars:
  never: {start: "00:00", end: "00:00"}
kpis:
  - {id: wait, value: 0, target: 4, direction: lower, owner: floor, min: 0, source: {kind: webhook-in, name: queue, field: wait_min}}
  - {id: cashiers, value: 1, owner: floor, source: {kind: manual}}
  - {id: margin, value: 0.27, target: 0.25, direction: higher, owner: owner}
  - {id: stockout, value: 0.03, target: 0.02, direction: lower, owner: buyer}
edges:
  - {from: cashiers, to: wait, weight: -0.6}
actions:
  - id: second_counter
    title: Open a second counter
    adapter: noop
    window: never
    effects: [{kpi: cashiers, change: 1, mode: absolute}]
  - id: reorder
    title: Raise PO
    adapter: webhook
    preconditions: [{kpi: stockout, worse_than: 0.02}]
    compensate: cancel_po
    effects: [{kpi: stockout, change: -0.4}]
    webhook: {url: "${ZYNTRA_TEST_ERP}/po", body: {gap: "gap:stockout"}}
  - id: cancel_po
    title: Cancel PO
    adapter: webhook
    effects: [{kpi: stockout, change: 0.3}]
    webhook: {url: "${ZYNTRA_TEST_ERP}/po/cancel"}
  - id: deep_markdown
    title: Deep markdown
    adapter: noop
    invariants: [{kpi: margin, max_worsen: 0.03}]
    effects: [{kpi: margin, change: -0.1}, {kpi: stockout, change: -0.2}]
`

func setupCounter(t *testing.T) *fixture {
	t.Helper()
	return setupWith(t, counter, func(o *Options) {
		o.Inputs, _ = inputs.Open("")
		o.Auth.SetIngestToken("ingest-tok")
		cfg := adapters.Config{Inputs: o.Inputs}
		o.Refresh = func(ctx context.Context, m *graph.Model) (adapters.Report, error) {
			return adapters.Refresh(ctx, m, cfg)
		}
	})
}

func TestIngestAndManualValues(t *testing.T) {
	f := setupCounter(t)
	body := `{"wait_min": 6.5, "counter": 1}`
	if c := f.do(t, "POST", "/api/v1/ingest/queue", "ingest-tok", body, nil); c != 202 {
		t.Fatalf("ingest = %d", c)
	}
	if c := f.do(t, "POST", "/api/v1/ingest/nope", "ingest-tok", body, nil); c != 404 {
		t.Fatalf("unknown channel = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/ingest/queue", "v", viewer, body, nil); c != 403 {
		t.Fatalf("viewer ingest = %d", c)
	}
	if c := f.do(t, "GET", "/api/v1/gaps", "ingest-tok", "", nil); c != 403 {
		t.Fatalf("the ingest token must not read the API: %d", c)
	}
	m, _ := f.s.snapshot()
	if k, _ := m.KPI("wait"); k.Value != 6.5 {
		t.Fatalf("wait after ingest = %v", k.Value)
	}

	if c := f.as(t, "POST", "/api/v1/kpis/cashiers/value", "v", viewer, `{"value": 2}`, nil); c != 403 {
		t.Fatalf("viewer manual value = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/kpis/margin/value", "floor", proposer, `{"value": 2}`, nil); c != 400 {
		t.Fatalf("non-manual KPI = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/kpis/cashiers/value", "floor", proposer, `{"reason": "x"}`, nil); c != 400 {
		t.Fatalf("missing value = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/kpis/cashiers/value", "floor", proposer, `{"value": 2, "reason": "evening rush"}`, nil); c != 200 {
		t.Fatalf("manual value = %d", c)
	}
	m, _ = f.s.snapshot()
	if k, _ := m.KPI("cashiers"); k.Value != 2 {
		t.Fatalf("cashiers after manual entry = %v", k.Value)
	}
	var audit struct {
		Events []approvals.Event      `json:"events"`
		Chain  approvals.Verification `json:"chain"`
	}
	f.do(t, "GET", "/api/v1/audit", "k3y", "", &audit)
	found := false
	for _, e := range audit.Events {
		if e.Action == "manual:cashiers" && e.By == "floor" && strings.Contains(e.Note, "evening rush") {
			found = true
		}
	}
	if !found || !audit.Chain.OK {
		t.Fatalf("manual entry not audited: %+v", audit)
	}
	var in struct {
		Manual []struct {
			KPI   string         `json:"kpi"`
			Entry *inputs.Manual `json:"entry"`
		} `json:"manual"`
	}
	f.do(t, "GET", "/api/v1/inputs", "k3y", "", &in)
	if len(in.Manual) != 1 || in.Manual[0].Entry == nil || in.Manual[0].Entry.By != "floor" {
		t.Fatalf("inputs = %+v", in)
	}
}

func TestOwnerFilters(t *testing.T) {
	f := setupCounter(t)
	f.do(t, "POST", "/api/v1/ingest/queue", "ingest-tok", `{"wait_min": 6.5}`, nil)
	var g struct {
		Gaps []struct {
			KPI   string `json:"kpi"`
			Owner string `json:"owner"`
		} `json:"gaps"`
		Owners []string `json:"owners"`
	}
	f.do(t, "GET", "/api/v1/gaps?owner=floor", "k3y", "", &g)
	if len(g.Gaps) != 1 || g.Gaps[0].KPI != "wait" || len(g.Owners) != 3 {
		t.Fatalf("floor gaps = %+v", g)
	}
	var p struct {
		Recommendations []struct {
			Action string `json:"action"`
		} `json:"recommendations"`
	}
	f.do(t, "GET", "/api/v1/plan?owner=buyer", "k3y", "", &p)
	for _, r := range p.Recommendations {
		if strings.Contains(r.Action, "second_counter") && !strings.Contains(r.Action, "+") {
			t.Errorf("buyer plan includes a floor-only action: %s", r.Action)
		}
	}
	if len(p.Recommendations) == 0 {
		t.Fatal("buyer plan is empty")
	}
}

func TestWebhookProposalPreconditionsAndWindows(t *testing.T) {
	f := setupCounter(t)
	var p approvals.Proposal
	if c := f.as(t, "POST", "/api/v1/proposals", "buyer", proposer, `{"action":"reorder"}`, &p); c != 201 {
		t.Fatalf("propose = %d", c)
	}
	if len(p.Kinds) != 1 || p.Kinds[0] != "webhook" || len(p.Compensate) != 1 || p.Compensate[0] != "cancel_po" {
		t.Fatalf("proposal kinds/compensate = %v %v", p.Kinds, p.Compensate)
	}
	if !strings.Contains(p.Render, "POST ${ZYNTRA_TEST_ERP}/po") || !strings.Contains(p.Render, `"kpi": "stockout"`) {
		t.Fatalf("render:\n%s", p.Render)
	}
	var done approvals.Proposal
	if c := f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "boss", approver, `{}`, &done); c != 200 {
		t.Fatalf("approve = %d %+v", c, done)
	}
	if done.Status != approvals.Executed || done.Execution == nil || !strings.Contains(done.Execution.Output, "dry-run: would send POST") {
		t.Fatalf("executed = %+v", done.Execution)
	}

	if c := f.as(t, "POST", "/api/v1/proposals", "x", proposer, `{"action":"deep_markdown"}`, nil); c != 422 {
		t.Fatalf("invariant break should be refused, got %d", c)
	}
	f.setKPI("stockout", 0.01)
	var refused map[string]any
	if c := f.as(t, "POST", "/api/v1/proposals", "x", proposer, `{"action":"reorder"}`, &refused); c != 422 ||
		!strings.Contains(strings.Join(toStrings(refused["blocked_reasons"]), ";"), "precondition") {
		t.Fatalf("failed precondition should be refused, got %d %v", c, refused)
	}

	var sc approvals.Proposal
	f.as(t, "POST", "/api/v1/proposals", "floor", proposer, `{"action":"second_counter"}`, &sc)
	if sc.Policy == nil || len(sc.Policy.Windows) != 1 || sc.Policy.Windows[0] != "never" {
		t.Fatalf("action window not in policy: %+v", sc.Policy)
	}
	f.as(t, "POST", "/api/v1/proposals/"+sc.ID+"/approve", "boss", approver, `{}`, &done)
	if done.Status != approvals.Approved || !done.WaitingForWindow {
		t.Fatalf("outside its window the action must wait: %s waiting=%v", done.Status, done.WaitingForWindow)
	}
}

func toStrings(v any) []string {
	var out []string
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
