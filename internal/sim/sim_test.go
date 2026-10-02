// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package sim

import (
	"math"
	"strings"
	"testing"
	"time"

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

const richModel = `
kpis:
  - {id: replicas, value: 0}
  - {id: lat, value: 400, target: 300, direction: lower}
  - {id: avail, value: 99.5, target: 99.9, direction: higher, max: 100, criticality: critical}
  - {id: cost, value: 100, target: 150, direction: lower}
edges:
  - {from: replicas, to: lat, weight: -0.1, confidence: 0.5, delay: 2m}
  - {from: lat, to: avail, weight: -0.05, delay: 1m}
  - {from: replicas, to: cost, weight: 0.1}
constraints:
  - {kpi: cost, ceiling: 140, why: budget}
actions:
  - {id: scale, effects: [{kpi: replicas, change: 2, mode: absolute, delay: 30s}]}
  - {id: boost, effects: [{kpi: avail, change: 1, mode: absolute}]}
  - {id: tune, effects: [{kpi: lat, change: -0.9, saturation: 0.3, uncertainty: 0.1}]}
  - {id: splurge, effects: [{kpi: cost, change: 0.5}]}
`

func loadRich(t *testing.T) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(richModel))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAbsoluteFromZeroAndDelays(t *testing.T) {
	m := loadRich(t)
	r, err := Simulate(m, "scale")
	if err != nil {
		t.Fatal(err)
	}
	// replicas 0 -> 2; relative change against 1 unit is +200%.
	if k := after(r, "replicas"); !near(k.After, 2) || !near(k.Change, 2) {
		t.Fatalf("replicas %+v", k)
	}
	// lat -0.1 * 200% = -20% (band ±50% of the weight: -10%..-30%).
	if k := after(r, "lat"); !near(k.After, 320) || !near(k.Low, 280) || !near(k.High, 360) {
		t.Fatalf("lat %+v", k)
	}
	if k := after(r, "avail"); k.SettlesAfter.D() != 3*time.Minute+30*time.Second {
		t.Fatalf("avail settle %v", k.SettlesAfter.D())
	}
	if r.SettlesAfter.D() != 3*time.Minute+30*time.Second {
		t.Fatalf("plan settle %v", r.SettlesAfter.D())
	}
	if r.Uncertainty() <= 0 {
		t.Fatalf("expected a band, got %v", r.Uncertainty())
	}
}

func TestBoundsClampAvailability(t *testing.T) {
	m := loadRich(t)
	r, err := Simulate(m, "boost")
	if err != nil {
		t.Fatal(err)
	}
	if k := after(r, "avail"); !near(k.After, 100) || !k.Bounded {
		t.Fatalf("avail %+v", k)
	}
	found := false
	for _, s := range r.Trace {
		if strings.Contains(s.Text, "held at 100") {
			found = true
		}
	}
	if !found {
		t.Fatalf("trace lacks bound step: %+v", r.Trace)
	}
}

func TestSaturationAndUncertainty(t *testing.T) {
	m := loadRich(t)
	r, err := Simulate(m, "tune")
	if err != nil {
		t.Fatal(err)
	}
	k := after(r, "lat")
	// -0.9 saturating toward 0.3: about -0.285, never past -0.3.
	if k.Change > -0.28 || k.Change < -0.3 {
		t.Fatalf("lat change %v", k.Change)
	}
	if !(k.Low < k.After && k.After < k.High) {
		t.Fatalf("lat band %+v", k)
	}
}

func TestPlanCombinesAndChecksConstraints(t *testing.T) {
	m := loadRich(t)
	a, _ := m.Action("scale")
	b, _ := m.Action("tune")
	r, err := ApplyPlan(m, []graph.Action{*a, *b}, Options{Unusable: map[string]bool{"lat": true, "cost": true}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Action != "scale+tune" || len(r.Actions) != 2 {
		t.Fatalf("plan id %q %v", r.Action, r.Actions)
	}
	single, _ := Simulate(m, "scale")
	if after(r, "lat").After >= after(single, "lat").After {
		t.Fatal("combined plan should lower latency more than one action")
	}
	if len(r.StaleInputs) != 2 || r.StaleInputs[0] != "cost" || r.StaleInputs[1] != "lat" {
		t.Fatalf("stale inputs %v", r.StaleInputs)
	}
	if len(r.Violations) != 0 {
		t.Fatalf("unexpected violations %+v", r.Violations)
	}

	bad, err := Simulate(m, "splurge")
	if err != nil {
		t.Fatal(err)
	}
	if len(bad.Violations) != 1 || bad.Violations[0].Kind != "ceiling" || bad.Violations[0].KPI != "cost" {
		t.Fatalf("violations %+v", bad.Violations)
	}
}

func TestCriticalWeighting(t *testing.T) {
	m := loadRich(t)
	r, err := Simulate(m, "boost")
	if err != nil {
		t.Fatal(err)
	}
	if !(r.WeightedImprovement() > r.Improvement()) {
		t.Fatalf("critical kpi should weigh more: weighted %v plain %v", r.WeightedImprovement(), r.Improvement())
	}
}
