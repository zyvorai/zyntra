// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package calibrate

import (
	"fmt"
	"math"
	"strings"

	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/sim"
)

// actionSample is one single-action decision's evidence about one KPI: how
// much of the predicted change was the action's own direct effect.
type actionSample struct {
	actualRel, predRel, direct float64
}

// ActionSuggestion is a proposed correction to one action's direct effect on
// one KPI. Like edge suggestions it is never applied automatically.
type ActionSuggestion struct {
	Action      string  `json:"action"`
	KPI         string  `json:"kpi"`
	N           int     `json:"n"`
	Declared    float64 `json:"declared"`
	Scale       float64 `json:"scale"`
	Suggested   float64 `json:"suggested"`
	Before      float64 `json:"loo_error_before"`
	After       float64 `json:"loo_error_after"`
	Improvement float64 `json:"improvement"`
	YAML        string  `json:"yaml"`
	Why         string  `json:"why"`
}

// directRelative returns the relative direct effect the simulation predicted
// on kpi. Absolute-mode effects and bound clamps are not relative changes, so
// they are left out, and a KPI with more than one direct step (an action that
// lists it twice) is ambiguous.
func directRelative(r *sim.Result, kpi string) (float64, bool) {
	var d float64
	n := 0
	for _, st := range r.Trace {
		if st.From != "" || st.To != kpi || !strings.Contains(st.Text, "(direct effect") {
			continue
		}
		if strings.Contains(st.Text, "direct effect, absolute") {
			return 0, false
		}
		d += st.Delta
		n++
	}
	return d, n == 1 && d != 0
}

// suggestAction fits one scale for the direct effect, holding the edge
// contributions at their predicted size, with the same shrinkage, held-out
// error test and significance test the edge fit uses.
func suggestAction(m *graph.Model, action, kpi string, ss []actionSample, opt Options) (*ActionSuggestion, string) {
	if len(ss) < opt.MinSamples {
		return nil, fmt.Sprintf("%d single-action decision(s); %d are needed", len(ss), opt.MinSamples)
	}
	a, ok := m.Action(action)
	if !ok {
		return nil, "the action is no longer in the model"
	}
	var ef *graph.Effect
	for i := range a.Effects {
		if a.Effects[i].KPI == kpi && a.Effects[i].Mode != graph.EffectAbsolute {
			ef = &a.Effects[i]
		}
	}
	if ef == nil {
		return nil, "the action no longer has a relative effect on this KPI"
	}
	y := make([]float64, len(ss))
	c := make([][]float64, len(ss))
	for i, s := range ss {
		c[i] = []float64{s.direct}
		y[i] = s.actualRel - (s.predRel - s.direct)
	}
	scales := ridge(c, y, nil, opt.PriorDecisions)
	var before, after, rss float64
	for i := range ss {
		before += math.Abs(y[i] - c[i][0])
		loo := ridge(c, y, []int{i}, opt.PriorDecisions)
		after += math.Abs(y[i] - clamp(loo[0], opt.MinScale, opt.MaxScale)*c[i][0])
		rss += (y[i] - scales[0]*c[i][0]) * (y[i] - scales[0]*c[i][0])
	}
	n := float64(len(ss))
	before, after = before/n, after/n
	if before < 1e-9 {
		return nil, "the model already predicts these decisions"
	}
	improvement := 1 - after/before
	if improvement < opt.MinImprovement {
		return nil, fmt.Sprintf("a correction would improve held-out error by only %.0f%% (need %.0f%%)", improvement*100, opt.MinImprovement*100)
	}
	var sxx float64
	for i := range ss {
		sxx += c[i][0] * c[i][0]
	}
	se := math.Inf(1)
	if sxx > 0 {
		se = math.Sqrt(rss/math.Max(n-1, 1)) / math.Sqrt(sxx)
	}
	s := clamp(scales[0], opt.MinScale, opt.MaxScale)
	if math.Abs(s-1) < 0.05 || math.Abs(scales[0]-1) < 2*se {
		return nil, "the fitted scale is within 5% of the declared effect, or not clearly different from it"
	}
	sug := &ActionSuggestion{Action: action, KPI: kpi, N: len(ss), Declared: ef.Change, Scale: round(s), Suggested: round3(ef.Change * s),
		Before: round(before), After: round(after), Improvement: round(improvement)}
	sug.YAML = fmt.Sprintf("  # action %s\n  - { kpi: %s, change: %g } # was %g", action, kpi, sug.Suggested, ef.Change)
	sug.Why = fmt.Sprintf("Across %d decisions that ran only %s, %s moved about %.2gx as much as the declared effect; held-out error falls from %.3g to %.3g (%.0f%% better).",
		len(ss), action, kpi, s, before, after, improvement*100)
	return sug, ""
}
