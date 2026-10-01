// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package sim runs deterministic what-if simulations over the KPI graph.
// Every predicted change is traceable to an action effect or an edge.
package sim

import (
	"fmt"
	"math"

	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
)

type KPIResult struct {
	KPI         string  `json:"kpi"`
	Name        string  `json:"name"`
	Unit        string  `json:"unit,omitempty"`
	Before      float64 `json:"before"`
	After       float64 `json:"after"`
	Change      float64 `json:"change"`
	MetBefore   bool    `json:"met_before"`
	MetAfter    bool    `json:"met_after"`
	HasTarget   bool    `json:"has_target"`
	SeverityOld float64 `json:"severity_before"`
	SeverityNew float64 `json:"severity_after"`
}

// Step is one link in the explanation trace.
type Step struct {
	From   string  `json:"from,omitempty"`
	To     string  `json:"to"`
	Weight float64 `json:"weight,omitempty"`
	Delta  float64 `json:"delta"`
	Text   string  `json:"text"`
}

type Result struct {
	Action         string      `json:"action"`
	ActionName     string      `json:"action_name"`
	KPIs           []KPIResult `json:"kpis"`
	Trace          []Step      `json:"trace"`
	SeverityBefore float64     `json:"severity_before"`
	SeverityAfter  float64     `json:"severity_after"`
	GapsClosed     []string    `json:"gaps_closed"`
	GapsOpened     []string    `json:"gaps_opened"`
}

// Improvement is the reduction in total gap severity. Negative means the
// action makes things worse overall.
func (r Result) Improvement() float64 { return r.SeverityBefore - r.SeverityAfter }

// Simulate applies an action's direct effects and propagates relative changes
// along edges in topological order. The model is not modified.
func Simulate(m *graph.Model, actionID string) (Result, error) {
	a, ok := m.Action(actionID)
	if !ok {
		return Result{}, fmt.Errorf("unknown action %q", actionID)
	}
	return Apply(m, *a)
}

func Apply(m *graph.Model, a graph.Action) (Result, error) {
	order, err := m.TopoOrder()
	if err != nil {
		return Result{}, err
	}
	res := Result{Action: a.ID, ActionName: a.Name}

	delta := make(map[string]float64, len(m.KPIs))
	for _, ef := range a.Effects {
		if _, ok := m.KPI(ef.KPI); !ok {
			return Result{}, fmt.Errorf("action %q: unknown kpi %q", a.ID, ef.KPI)
		}
		delta[ef.KPI] += ef.Change
		res.Trace = append(res.Trace, Step{
			To: ef.KPI, Delta: ef.Change,
			Text: fmt.Sprintf("%s changes %s by %s (direct effect)", a.ID, ef.KPI, Pct(ef.Change)),
		})
	}

	in := make(map[string][]graph.Edge)
	for _, e := range m.Edges {
		in[e.To] = append(in[e.To], e)
	}
	for _, id := range order {
		for _, e := range in[id] {
			d := delta[e.From]
			if d == 0 || e.Weight == 0 {
				continue
			}
			contrib := e.Weight * d
			delta[id] += contrib
			text := fmt.Sprintf("%s %s -> %s %s (weight %+.3g)", e.From, Pct(d), id, Pct(contrib), e.Weight)
			if e.Why != "" {
				text += ": " + e.Why
			}
			res.Trace = append(res.Trace, Step{From: e.From, To: id, Weight: e.Weight, Delta: contrib, Text: text})
		}
		if delta[id] < -1 {
			delta[id] = -1
		}
	}

	after := make(map[string]float64, len(m.KPIs))
	for _, k := range m.KPIs {
		v := k.Value * (1 + delta[k.ID])
		after[k.ID] = v
		sb, sa := gaps.Severity(k, k.Value), gaps.Severity(k, v)
		kr := KPIResult{
			KPI: k.ID, Name: k.Name, Unit: k.Unit,
			Before: k.Value, After: v, Change: delta[k.ID],
			HasTarget: k.Target != nil, MetBefore: sb == 0, MetAfter: sa == 0,
			SeverityOld: sb, SeverityNew: sa,
		}
		res.KPIs = append(res.KPIs, kr)
		if kr.HasTarget && !kr.MetBefore && kr.MetAfter {
			res.GapsClosed = append(res.GapsClosed, k.ID)
		}
		if kr.HasTarget && kr.MetBefore && !kr.MetAfter {
			res.GapsOpened = append(res.GapsOpened, k.ID)
		}
	}
	res.SeverityBefore = gaps.Total(m, nil)
	res.SeverityAfter = gaps.Total(m, after)
	return res, nil
}

// Pct formats a relative change, keeping precision for sub-1% moves.
func Pct(x float64) string {
	if x != 0 && math.Abs(x) < 0.01 {
		return fmt.Sprintf("%+.2f%%", x*100)
	}
	return fmt.Sprintf("%+.1f%%", x*100)
}
