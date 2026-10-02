// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package outcome

import (
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

const model = `
kpis:
  - {id: wait, value: 30, target: 15, direction: lower}
  - {id: headroom, value: 10, direction: higher}
  - {id: avail, value: 99.9, target: 99.5, direction: higher, criticality: critical, max: 100}
  - {id: cost, value: 100, direction: lower}
constraints:
  - {kpi: cost, ceiling: 120}
actions:
  - {id: a, name: A, effects: [{kpi: wait, change: -0.6}]}
  - id: b
    name: B
    effects: [{kpi: headroom, change: 0.5}]
    outcome:
      window: 5m
      samples: 3
      successCriteria: [{kpi: headroom, op: ">=", value: 14}]
      guardrails: [wait]
      tolerance: 0.1
`

func load(t *testing.T) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func set(m *graph.Model, id string, v float64) {
	k, _ := m.KPI(id)
	k.Value = v
}

func values(m *graph.Model) map[string]float64 {
	out := map[string]float64{}
	for _, k := range m.KPIs {
		out[k.ID] = k.Value
	}
	return out
}

func TestDefaultSpec(t *testing.T) {
	m := load(t)
	a, _ := m.Action("a")
	s := SpecFor(m, []graph.Action{*a}, values(m), map[string]float64{"wait": 12})
	if s.Window != DefaultWindow || s.Samples != DefaultSamples || s.Tolerance != DefaultTolerance {
		t.Fatalf("defaults %+v", s)
	}
	if len(s.Success) != 1 || s.Success[0].KPI != "wait" || s.Success[0].Op != "met" {
		t.Fatalf("success %+v", s.Success)
	}
	if len(s.Guardrails) != 2 || s.Guardrails[0] != "avail" || s.Guardrails[1] != "cost" {
		t.Fatalf("guardrails %v", s.Guardrails)
	}
	b, _ := m.Action("b")
	s = SpecFor(m, []graph.Action{*b}, values(m), nil)
	if s.Window != 5*time.Minute || s.Samples != 3 || s.Tolerance != 0.1 || len(s.Guardrails) != 1 {
		t.Fatalf("explicit %+v", s)
	}
}

func TestVerifiedAfterConsecutiveFreshSamples(t *testing.T) {
	m := load(t)
	a, _ := m.Action("a")
	t0 := time.Unix(1_800_000_000, 0)
	r := Start(SpecFor(m, []graph.Action{*a}, values(m), map[string]float64{"wait": 12}), values(m), t0)
	set(m, "wait", 14)
	if r.Observe(m, nil, t0.Add(time.Minute)) {
		t.Fatal("one sample must not verify")
	}
	if r.Observe(m, map[string]bool{"wait": true}, t0.Add(2*time.Minute)) {
		t.Fatal("stale sample must not count")
	}
	r.Observe(m, nil, t0.Add(3*time.Minute))
	if !r.Observe(m, nil, t0.Add(4*time.Minute)) || r.State != Verified {
		t.Fatalf("state %s samples %+v", r.State, r.Samples)
	}
	if r.Observe(m, nil, t0.Add(5*time.Minute)) {
		t.Fatal("observed after verdict")
	}
}

func TestGuardrailAndConstraintRegress(t *testing.T) {
	m := load(t)
	a, _ := m.Action("a")
	t0 := time.Unix(1_800_000_000, 0)
	r := Start(SpecFor(m, []graph.Action{*a}, values(m), map[string]float64{"wait": 12}), values(m), t0)
	set(m, "avail", 99.85)
	if r.Observe(m, nil, t0.Add(time.Minute)) {
		t.Fatalf("small dip within tolerance regressed: %v", r.Reasons)
	}
	set(m, "cost", 130)
	if !r.Observe(m, nil, t0.Add(2*time.Minute)) || r.State != Regressed {
		t.Fatalf("state %s", r.State)
	}
	if len(r.Reasons) != 2 {
		t.Fatalf("reasons %v", r.Reasons)
	}
}

func TestWindowEndMissedOrInconclusive(t *testing.T) {
	m := load(t)
	a, _ := m.Action("a")
	t0 := time.Unix(1_800_000_000, 0)
	spec := SpecFor(m, []graph.Action{*a}, values(m), map[string]float64{"wait": 12})
	r := Start(spec, values(m), t0)
	r.Observe(m, nil, t0.Add(time.Minute))
	if !r.Observe(m, nil, t0.Add(DefaultWindow)) || r.State != Missed {
		t.Fatalf("state %s", r.State)
	}
	r = Start(spec, values(m), t0)
	if !r.Observe(m, map[string]bool{"wait": true}, t0.Add(DefaultWindow+time.Second)) || r.State != Inconclusive {
		t.Fatalf("stale at window end: %s", r.State)
	}
}
