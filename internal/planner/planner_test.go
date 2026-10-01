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
