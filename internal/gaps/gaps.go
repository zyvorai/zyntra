// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package gaps finds KPIs that miss their targets and scores how far off they are.
package gaps

import (
	"math"
	"sort"

	"github.com/zyvorai/zyntra/internal/graph"
)

type Gap struct {
	KPI       string  `json:"kpi"`
	Name      string  `json:"name"`
	Owner     string  `json:"owner,omitempty"`
	Unit      string  `json:"unit,omitempty"`
	Value     float64 `json:"value"`
	Target    float64 `json:"target"`
	Direction string  `json:"direction"`
	// Severity is the relative shortfall against target: 0.25 means 25% off.
	Severity float64 `json:"severity"`
}

// Severity returns the relative shortfall of value against the KPI target, or
// 0 when the target is met or the KPI has no target.
func Severity(k graph.KPI, value float64) float64 {
	if k.Target == nil {
		return 0
	}
	t := *k.Target
	denom := math.Abs(t)
	if denom == 0 {
		denom = 1
	}
	var short float64
	switch k.Direction {
	case graph.HigherIsBetter:
		short = t - value
	case graph.LowerIsBetter:
		short = value - t
	}
	if short <= 0 {
		return 0
	}
	return short / denom
}

// Detect returns every KPI that misses its target, worst first.
func Detect(m *graph.Model) []Gap {
	var out []Gap
	for _, k := range m.KPIs {
		s := Severity(k, k.Value)
		if s == 0 {
			continue
		}
		out = append(out, Gap{
			KPI: k.ID, Name: k.Name, Owner: k.Owner, Unit: k.DisplayUnit(),
			Value: k.Value, Target: *k.Target, Direction: string(k.Direction),
			Severity: s,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Severity > out[j].Severity })
	return out
}

// Total sums severity across all KPIs for the given values (by KPI id). KPIs
// missing from values use the model value.
func Total(m *graph.Model, values map[string]float64) float64 {
	var sum float64
	for _, k := range m.KPIs {
		v, ok := values[k.ID]
		if !ok {
			v = k.Value
		}
		sum += Severity(k, v)
	}
	return sum
}

// WeightedTotal is Total with each KPI's severity multiplied by its
// criticality weight (critical 4, high 2, normal 1, low 0.5).
func WeightedTotal(m *graph.Model, values map[string]float64) float64 {
	var sum float64
	for _, k := range m.KPIs {
		v, ok := values[k.ID]
		if !ok {
			v = k.Value
		}
		sum += Severity(k, v) * k.Weight()
	}
	return sum
}

// ForOwner keeps the gaps owned by owner; an empty owner keeps all.
func ForOwner(g []Gap, owner string) []Gap {
	if owner == "" {
		return g
	}
	out := []Gap{}
	for _, x := range g {
		if x.Owner == owner {
			out = append(out, x)
		}
	}
	return out
}
