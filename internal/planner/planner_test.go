// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package planner

import (
	"testing"

	"github.com/zyvorai/zyntra/internal/graph"
)

func TestPlanRanksAndFilters(t *testing.T) {
	m, err := graph.Parse([]byte(`
kpis:
  - {id: lat, value: 400, target: 300, direction: lower}
actions:
  - {id: risky, risk: high, effects: [{kpi: lat, change: -0.25}]}
  - {id: safe, risk: low, effects: [{kpi: lat, change: -0.2}]}
  - {id: worse, effects: [{kpi: lat, change: 0.1}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	recs, err := Plan(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 recs (worse dropped), got %d", len(recs))
	}
	// risky gains 0.333, safe gains 0.2667; high-risk penalty 0.15 puts safe first.
	if recs[0].Action != "safe" || recs[1].Action != "risky" {
		t.Fatalf("order = %s, %s", recs[0].Action, recs[1].Action)
	}
	for i, r := range recs {
		if r.Rank != i+1 || r.Status != StatusPendingApproval {
			t.Fatalf("rec %d: %+v", i, r)
		}
	}
}

const constrained = `
kpis:
  - {id: avail, value: 99.95, target: 99.9, direction: higher, criticality: critical, max: 100}
  - {id: cost, value: 200, target: 100, direction: lower}
  - {id: lat, value: 400, target: 300, direction: lower}
edges:
  - {from: cost, to: avail, weight: 0.002}
actions:
  - {id: slash, name: Slash spend, effects: [{kpi: cost, change: -0.5}]}
  - {id: trim, name: Trim spend, effects: [{kpi: cost, change: -0.1}]}
  - {id: cache, name: Cache, effects: [{kpi: lat, change: -0.3}]}
`

func TestConstraintBlocksHigherScoringAction(t *testing.T) {
	m, err := graph.Parse([]byte(constrained))
	if err != nil {
		t.Fatal(err)
	}
	r, err := PlanWith(m, Options{MaxCombo: 1})
	if err != nil {
		t.Fatal(err)
	}
	// slash closes the cost gap but drops avail 99.95 -> 99.85, below the
	// critical target: blocked regardless of score.
	if len(r.Blocked) != 1 || r.Blocked[0].Action != "slash" || len(r.Blocked[0].BlockedReasons) == 0 {
		t.Fatalf("blocked %+v", r.Blocked)
	}
	for _, rec := range r.Recommendations {
		if rec.Action == "slash" {
			t.Fatal("blocked action ranked")
		}
	}
	if len(r.Recommendations) != 2 {
		t.Fatalf("recs %+v", r.Recommendations)
	}
}

func TestPairsStaleAndConfidence(t *testing.T) {
	m, err := graph.Parse([]byte(constrained))
	if err != nil {
		t.Fatal(err)
	}
	r, err := PlanWith(m, Options{Unusable: map[string]bool{"lat": true}})
	if err != nil {
		t.Fatal(err)
	}
	var pair *Recommendation
	for i := range r.Recommendations {
		if r.Recommendations[i].Action == "trim+cache" {
			pair = &r.Recommendations[i]
		}
	}
	if pair == nil || len(pair.Actions) != 2 {
		t.Fatalf("missing pair in %+v", r.Recommendations)
	}
	if r.Recommendations[0].Action != "trim+cache" {
		t.Fatalf("pair should rank first: %+v", r.Recommendations[0])
	}
	if pair.Confidence != ConfidenceLow || len(pair.StaleInputs) != 1 {
		t.Fatalf("pair confidence %+v", pair)
	}
	for _, rec := range r.Recommendations {
		if rec.Action == "trim" && rec.Confidence != ConfidenceHigh {
			t.Fatalf("trim should be high confidence: %+v", rec)
		}
	}
}
