// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package sim

import (
	"math"
	"testing"

	"github.com/zyvorai/zyntra/internal/graph"
)

const model = `
kpis:
  - {id: nodes, value: 10}
  - {id: headroom, value: 10, target: 20, direction: higher}
  - {id: wait, value: 30, target: 15, direction: lower}
  - {id: cost, value: 100, target: 110, direction: lower}
edges:
  - {from: nodes, to: headroom, weight: 2}
  - {from: nodes, to: cost, weight: 1}
  - {from: headroom, to: wait, weight: -0.5}
actions:
  - {id: grow, name: Grow, effects: [{kpi: nodes, change: 0.5}]}
  - {id: tune, name: Tune, effects: [{kpi: headroom, change: 1}, {kpi: wait, change: -0.1}]}
`

func load(t *testing.T) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func after(r Result, id string) KPIResult {
	for _, k := range r.KPIs {
		if k.KPI == id {
			return k
		}
	}
	return KPIResult{}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPropagation(t *testing.T) {
	m := load(t)
	r, err := Simulate(m, "grow")
	if err != nil {
		t.Fatal(err)
	}
	// nodes +50% -> headroom +100% -> wait -50%; cost +50%.
	if k := after(r, "headroom"); !near(k.After, 20) || !k.MetAfter {
		t.Fatalf("headroom %+v", k)
	}
	if k := after(r, "wait"); !near(k.After, 15) {
		t.Fatalf("wait %+v", k)
	}
	if k := after(r, "cost"); !near(k.After, 150) || k.MetAfter {
		t.Fatalf("cost %+v", k)
	}
	if len(r.GapsClosed) != 2 || len(r.GapsOpened) != 1 || r.GapsOpened[0] != "cost" {
		t.Fatalf("closed=%v opened=%v", r.GapsClosed, r.GapsOpened)
	}
	if len(r.Trace) != 4 {
		t.Fatalf("trace has %d steps: %+v", len(r.Trace), r.Trace)
	}
	if m.KPIs[1].Value != 10 {
		t.Fatal("simulation mutated the model")
	}
}

func TestDirectAndPropagatedEffectsSum(t *testing.T) {
	r, err := Simulate(load(t), "tune")
	if err != nil {
		t.Fatal(err)
	}
	// wait: -10% direct + (-0.5 * +100%) = -60%.
	if k := after(r, "wait"); !near(k.Change, -0.6) || !near(k.After, 12) {
		t.Fatalf("wait %+v", k)
	}
	if !near(r.SeverityAfter, 0) || r.Improvement() <= 0 {
		t.Fatalf("severity %v -> %v", r.SeverityBefore, r.SeverityAfter)
	}
}

func TestClampAndUnknown(t *testing.T) {
	m := load(t)
	r, err := Apply(m, graph.Action{ID: "kill", Effects: []graph.Effect{{KPI: "nodes", Change: -0.9}}})
	if err != nil {
		t.Fatal(err)
	}
	if k := after(r, "headroom"); k.After < 0 {
		t.Fatalf("headroom went negative: %+v", k)
	}
	if _, err := Simulate(m, "nope"); err == nil {
		t.Fatal("expected unknown action error")
	}
	if _, err := Apply(m, graph.Action{ID: "bad", Effects: []graph.Effect{{KPI: "zzz", Change: 1}}}); err == nil {
		t.Fatal("expected unknown kpi error")
	}
}
