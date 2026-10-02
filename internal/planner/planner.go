// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package planner ranks candidate actions, and pairs of actions, by
// simulated outcome, risk, uncertainty and input freshness. Candidates that
// breach a hard constraint are never ranked, however much they improve
// other KPIs. It only recommends; nothing it returns is executed without
// approval.
package planner

import (
	"sort"

	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/sim"
)

const (
	StatusPendingApproval = "pending-approval"
	StatusBlocked         = "blocked"
)

// Confidence labels.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

var riskPenalty = map[graph.Risk]float64{
	"":               0,
	graph.RiskLow:    0,
	graph.RiskMedium: 0.05,
	graph.RiskHigh:   0.15,
}

var riskOrder = map[graph.Risk]int{"": 0, graph.RiskLow: 1, graph.RiskMedium: 2, graph.RiskHigh: 3}

const (
	// UncertaintyWeight scales the width of the weighted-severity band into
	// a score penalty.
	UncertaintyWeight = 0.25
	// StalePenalty is subtracted per stale or missing input.
	StalePenalty = 0.1
)

type Recommendation struct {
	Rank int `json:"rank"`
	// Action is the action id, or ids joined with "+" for a pair.
	Action      string     `json:"action"`
	Actions     []string   `json:"actions"`
	Name        string     `json:"name"`
	Adapter     string     `json:"adapter,omitempty"`
	Risk        graph.Risk `json:"risk,omitempty"`
	Improvement float64    `json:"improvement"`
	// WeightedImprovement is the drop in criticality-weighted severity.
	WeightedImprovement float64        `json:"weighted_improvement"`
	Uncertainty         float64        `json:"uncertainty"`
	Score               float64        `json:"score"`
	Confidence          string         `json:"confidence"`
	StaleInputs         []string       `json:"stale_inputs,omitempty"`
	BlockedReasons      []string       `json:"blocked_reasons,omitempty"`
	SettlesAfter        graph.Duration `json:"settles_after,omitempty"`
	Status              string         `json:"status"`
	Result              sim.Result     `json:"result"`
}

// Options tune planning.
type Options struct {
	// Unusable marks KPIs whose current value is stale or missing.
	Unusable map[string]bool
	// MaxCombo is the largest number of actions combined into one plan:
	// 0 means the default (2), 1 disables combinations.
	MaxCombo int
}

// Result is a ranked plan plus the candidates that were blocked.
type Result struct {
	Recommendations []Recommendation `json:"recommendations"`
	Blocked         []Recommendation `json:"blocked"`
}

// Plan returns the ranked recommendations with default options.
func Plan(m *graph.Model) ([]Recommendation, error) {
	r, err := PlanWith(m, Options{})
	return r.Recommendations, err
}

// PlanWith simulates every action and every pair of actions that touch
// different KPIs, then ranks those that reduce weighted severity. Pairs are
// kept only when they beat both of their members.
func PlanWith(m *graph.Model, opt Options) (Result, error) {
	if opt.MaxCombo == 0 {
		opt.MaxCombo = 2
	}
	sopt := sim.Options{Unusable: opt.Unusable}
	var out Result
	singles := map[string]float64{}
	add := func(acts []graph.Action) error {
		r, err := sim.ApplyPlan(m, acts, sopt)
		if err != nil {
			return err
		}
		imp := r.WeightedImprovement()
		if imp <= 0 {
			return nil
		}
		if len(acts) > 1 {
			for _, a := range acts {
				if imp <= singles[a.ID] {
					return nil
				}
			}
		} else {
			singles[acts[0].ID] = imp
		}
		rec := build(acts, r)
		if rec.Status == StatusBlocked {
			out.Blocked = append(out.Blocked, rec)
		} else {
			out.Recommendations = append(out.Recommendations, rec)
		}
		return nil
	}
	for _, a := range m.Actions {
		if err := add([]graph.Action{a}); err != nil {
			return Result{}, err
		}
	}
	if opt.MaxCombo >= 2 {
		for i := range m.Actions {
			for j := i + 1; j < len(m.Actions); j++ {
				a, b := m.Actions[i], m.Actions[j]
				if overlap(a, b) {
					continue
				}
				if err := add([]graph.Action{a, b}); err != nil {
					return Result{}, err
				}
			}
		}
	}
	sortRank(out.Recommendations)
	sortRank(out.Blocked)
	if out.Recommendations == nil {
		out.Recommendations = []Recommendation{}
	}
	if out.Blocked == nil {
		out.Blocked = []Recommendation{}
	}
	return out, nil
}

func sortRank(recs []Recommendation) {
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].Score > recs[j].Score })
	for i := range recs {
		recs[i].Rank = i + 1
	}
}

func overlap(a, b graph.Action) bool {
	seen := map[string]bool{}
	for _, e := range a.Effects {
		seen[e.KPI] = true
	}
	for _, e := range b.Effects {
		if seen[e.KPI] {
			return true
		}
	}
	return false
}

func build(acts []graph.Action, r sim.Result) Recommendation {
	rec := Recommendation{
		Action: r.Action, Actions: r.Actions, Name: r.ActionName,
		Improvement: r.Improvement(), WeightedImprovement: r.WeightedImprovement(),
		Uncertainty: r.Uncertainty(), StaleInputs: r.StaleInputs, SettlesAfter: r.SettlesAfter,
		Status: StatusPendingApproval, Result: r,
	}
	risk := 0.0
	for _, a := range acts {
		risk += riskPenalty[a.Risk]
		if riskOrder[a.Risk] > riskOrder[rec.Risk] {
			rec.Risk = a.Risk
		}
		if rec.Adapter == "" {
			rec.Adapter = a.Adapter
		} else if a.Adapter != "" && a.Adapter != rec.Adapter {
			rec.Adapter = "multiple"
		}
	}
	rec.Score = rec.WeightedImprovement - risk - UncertaintyWeight*rec.Uncertainty -
		StalePenalty*float64(len(rec.StaleInputs))
	switch {
	case len(rec.StaleInputs) > 0 || rec.Uncertainty > rec.WeightedImprovement:
		rec.Confidence = ConfidenceLow
	case rec.Uncertainty > 0.25*rec.WeightedImprovement:
		rec.Confidence = ConfidenceMedium
	default:
		rec.Confidence = ConfidenceHigh
	}
	for _, v := range r.Violations {
		rec.BlockedReasons = append(rec.BlockedReasons, v.Text)
	}
	if len(rec.BlockedReasons) > 0 {
		rec.Status = StatusBlocked
	}
	return rec
}
