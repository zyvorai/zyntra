// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package planner ranks candidate actions by simulated outcome and risk.
// It only recommends; nothing it returns is executed without approval.
package planner

import (
	"sort"

	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/sim"
)

const StatusPendingApproval = "pending-approval"

var riskPenalty = map[graph.Risk]float64{
	"":               0,
	graph.RiskLow:    0,
	graph.RiskMedium: 0.05,
	graph.RiskHigh:   0.15,
}

type Recommendation struct {
	Rank        int        `json:"rank"`
	Action      string     `json:"action"`
	Name        string     `json:"name"`
	Adapter     string     `json:"adapter,omitempty"`
	Risk        graph.Risk `json:"risk,omitempty"`
	Improvement float64    `json:"improvement"`
	Score       float64    `json:"score"`
	Status      string     `json:"status"`
	Result      sim.Result `json:"result"`
}

// Plan simulates every action and returns them best first. Actions that do
// not reduce total gap severity are dropped.
func Plan(m *graph.Model) ([]Recommendation, error) {
	var recs []Recommendation
	for _, a := range m.Actions {
		r, err := sim.Apply(m, a)
		if err != nil {
			return nil, err
		}
		imp := r.Improvement()
		if imp <= 0 {
			continue
		}
		recs = append(recs, Recommendation{
			Action: a.ID, Name: a.Name, Adapter: a.Adapter, Risk: a.Risk,
			Improvement: imp, Score: imp - riskPenalty[a.Risk],
			Status: StatusPendingApproval, Result: r,
		})
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Score > recs[j].Score })
	for i := range recs {
		recs[i].Rank = i + 1
	}
	return recs, nil
}
