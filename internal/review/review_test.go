// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package review

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/outcome"
	"github.com/zyvorai/zyntra/internal/pack"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
)

func decided(id, action string, acc []outcome.Accuracy, state outcome.State) approvals.Proposal {
	now := time.Now()
	pred := map[string]float64{}
	for _, a := range acc {
		pred[a.KPI] = a.Predicted
	}
	return approvals.Proposal{ID: id, Action: action, Status: approvals.Executed, CreatedAt: now,
		Predicted: approvals.Prediction{KPIs: pred},
		Outcome:   &outcome.Record{State: state, DecidedAt: &now, Accuracy: acc, Predicted: pred}}
}

func TestExplainNamesTheEdgeThatOvershot(t *testing.T) {
	p := decided("prop-1", "add_capacity", []outcome.Accuracy{
		outcome.Score("capacity", 10, 18, 17.5),
		outcome.Score("queue_wait", 30, 12, 26),
		outcome.Score("latency", 400, 300, 390),
	}, outcome.Missed)
	p.Simulation = &sim.Result{Trace: []sim.Step{
		{To: "capacity", Delta: 0.8},
		{From: "capacity", To: "queue_wait", Weight: -0.75, Delta: -0.6},
		{From: "queue_wait", To: "latency", Weight: 0.4, Delta: -0.25},
	}}
	p.Inputs = &approvals.Inputs{
		Freshness: []freshness.State{{KPI: "latency", Status: freshness.Stale}},
		Sources:   []adapters.Status{{Name: "prometheus", State: "fallback", KPIs: []string{"latency"}}},
	}
	p.Outcome.Samples = []outcome.Sample{{Stale: []string{"latency"}}, {}}

	ex, ok := Explain(p)
	if !ok {
		t.Fatal("no explanation")
	}
	kinds := map[string]outcome.Finding{}
	for _, f := range ex.Findings {
		kinds[f.Kind+":"+f.KPI] = f
	}
	q, ok := kinds["edge-overshot:queue_wait"]
	if !ok || q.Edge != "capacity->queue_wait" || q.SuggestedWeight == nil || *q.SuggestedWeight > -0.1 || *q.SuggestedWeight < -0.3 {
		t.Fatalf("queue_wait finding: %+v\n%s", q, ex.Text)
	}
	if _, ok := kinds["inherited:latency"]; !ok {
		t.Errorf("latency should inherit queue_wait's miss: %s", ex.Text)
	}
	for _, k := range []string{"hit:capacity", "stale-input:latency", "stale-precondition:latency", "fallback-source:latency"} {
		if _, ok := kinds[k]; !ok {
			t.Errorf("missing %s in %s", k, ex.Text)
		}
	}
	if !VerifyHash(ex) {
		t.Fatal("hash does not verify")
	}
	ex.Findings[0].Text = "edited"
	if VerifyHash(ex) {
		t.Fatal("edited explanation still verifies")
	}
}

func TestSimilarFindsPrecedents(t *testing.T) {
	cur := approvals.Proposal{ID: "now", Action: "markdown_capped", Predicted: approvals.Prediction{KPIs: map[string]float64{"dead_stock_days": 50, "gross_margin": 0.26}}}
	a := decided("p1", "markdown_dead_stock", []outcome.Accuracy{outcome.Score("dead_stock_days", 62, 40, 61), outcome.Score("gross_margin", 0.27, 0.25, 0.2)}, outcome.Regressed)
	b := decided("p2", "markdown_dead_stock", []outcome.Accuracy{outcome.Score("dead_stock_days", 62, 40, 45), outcome.Score("gross_margin", 0.27, 0.25, 0.21)}, outcome.Regressed)
	c := decided("p3", "reorder_fast_movers", []outcome.Accuracy{outcome.Score("stockout_rate", 0.03, 0.018, 0.019)}, outcome.Verified)
	got := Similar([]approvals.Proposal{a, b, c, cur}, cur, 3)
	if len(got.Items) != 2 || got.Items[0].Action != "markdown_dead_stock" {
		t.Fatalf("%+v", got)
	}
	if !strings.Contains(got.Text, "markdown_dead_stock regressed 2 times") {
		t.Fatal(got.Text)
	}
}

func TestMissingEdgeFromHistory(t *testing.T) {
	m := &graph.Model{KPIs: []graph.KPI{{ID: "orders"}, {ID: "dock_dwell"}, {ID: "noise"}, {ID: "linked"}},
		Edges: []graph.Edge{{From: "orders", To: "linked", Weight: 0.5}}}
	hist := map[string][]ai.Point{}
	t0 := time.Now()
	orders := []float64{100, 120, 90, 130, 110, 140, 100, 150, 120, 95, 135, 105}
	noise := []float64{5, 5.1, 4.8, 5.3, 4.9, 5.0, 5.2, 4.7, 5.1, 5.0, 4.9, 5.2}
	for i, v := range orders {
		ts := t0.Add(time.Duration(i) * time.Minute)
		hist["orders"] = append(hist["orders"], ai.Point{T: ts, V: v})
		hist["linked"] = append(hist["linked"], ai.Point{T: ts, V: v * 2})
		hist["noise"] = append(hist["noise"], ai.Point{T: ts, V: noise[i]})
		dwell := 20.0
		if i > 0 {
			dwell = hist["dock_dwell"][i-1].V
		}
		if i > 1 {
			dwell *= 1 + 0.5*rel(orders[i-2], orders[i-1])
		}
		hist["dock_dwell"] = append(hist["dock_dwell"], ai.Point{T: ts, V: dwell})
	}
	got := MissingEdges(m, hist)
	var found *EdgeProposal
	for i := range got {
		if got[i].From == "orders" && got[i].To == "dock_dwell" {
			found = &got[i]
		}
		if got[i].To == "linked" || got[i].From == "linked" {
			if got[i].From == "orders" || got[i].To == "orders" {
				t.Fatalf("proposed an edge the model already has: %+v", got[i])
			}
		}
	}
	if found == nil || found.Direction != "leads" || found.Status != "proposed, not in the model" || !strings.Contains(found.YAML, "provenance: learned") {
		t.Fatalf("got %+v", got)
	}
}

func TestContradictionCheck(t *testing.T) {
	m, err := pack.Load("../../packs/shop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapters.Refresh(context.Background(), m, adapters.Config{}); err != nil {
		t.Fatal(err)
	}
	readme, _ := os.ReadFile("../../packs/shop/README.md")
	plan, _ := planner.Plan(m)
	rules := Rules(string(readme), m)
	if got := Check(m, plan, rules); len(got) != 0 {
		t.Fatalf("shop pack contradicts its README: %+v", got)
	}
	for i := range m.Actions {
		switch m.Actions[i].ID {
		case "reorder_fast_movers":
			m.Actions[i].Window, m.Actions[i].Preconditions = "", nil
		case "open_second_counter":
			m.Actions[i].Window = ""
		}
	}
	extra := string(readme) + "\n`drop_slow_supplier` must never be approved without the buyer present.\n"
	plan2, _ := planner.Plan(m)
	got := Check(m, plan2, Rules(extra, m))
	why := map[string]bool{}
	for _, c := range got {
		why[c.Action+":"+strings.SplitN(c.Why, ",", 2)[0]] = true
	}
	for _, want := range []string{
		"reorder_fast_movers:the rule limits reorder_fast_movers to the buy-hours window",
		"open_second_counter:the rule limits open_second_counter to the evening window",
		"drop_slow_supplier:the rule says it must not be approved",
	} {
		if !why[want] {
			t.Errorf("missing %q in %+v", want, got)
		}
	}
}
