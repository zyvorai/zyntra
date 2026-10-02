// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package calibrate backtests the model's edge weights against what actually
// happened after approved changes ran, and suggests corrections.
//
// For each KPI it regresses the observed relative change on the contributions
// its incoming edges were predicted to make (read from the proposal's stored
// simulation trace), shrinking toward the declared weight. A correction is
// suggested only when leave-one-out error improves by a clear margin. It
// never edits the model: a person reviews the suggestion and changes the pack.
//
// Limits it cannot remove: it needs applied changes with an observed outcome
// (a dry-run deployment has none), outcomes are confounded by anything else
// that moved in the window, and with several incoming edges the data may not
// separate them.
package calibrate

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/sim"
)

// Options tune how much evidence a suggestion needs.
type Options struct {
	// MinSamples is the fewest decisions a KPI needs (default 5).
	MinSamples int
	// MinImprovement is the leave-one-out error reduction required
	// (default 0.15, i.e. 15%).
	MinImprovement float64
	// PriorDecisions is how many decisions' worth of evidence the declared
	// weight counts for. The prior fades as real decisions accumulate
	// (default 3).
	PriorDecisions float64
	// MinScale and MaxScale bound how far a weight may move (default 0.25, 4).
	MinScale, MaxScale float64
}

func (o Options) withDefaults() Options {
	if o.MinSamples <= 0 {
		o.MinSamples = 5
	}
	if o.MinImprovement <= 0 {
		o.MinImprovement = 0.15
	}
	if o.PriorDecisions <= 0 {
		o.PriorDecisions = 3
	}
	if o.MinScale <= 0 {
		o.MinScale = 0.25
	}
	if o.MaxScale <= 0 {
		o.MaxScale = 4
	}
	return o
}

// KPIFit is the backtest of one KPI across decisions.
type KPIFit struct {
	KPI string `json:"kpi"`
	N   int    `json:"n"`
	// MeanAbsError is the mean absolute error of the predicted relative
	// change, and HitRate the share of decisions that moved the KPI the
	// predicted way.
	MeanAbsError float64 `json:"mean_abs_error"`
	HitRate      float64 `json:"hit_rate"`
	// Bias is the mean (actual - predicted) relative change: positive means
	// the model under-predicts improvement in that direction.
	Bias float64 `json:"bias"`
}

// EdgeFit is one edge's suggested correction.
type EdgeFit struct {
	From      string  `json:"from"`
	To        string  `json:"to"`
	Weight    float64 `json:"weight"`
	Scale     float64 `json:"scale"`
	Suggested float64 `json:"suggested"`
}

// Suggestion is a proposed change to the incoming edges of one KPI.
type Suggestion struct {
	KPI    string    `json:"kpi"`
	N      int       `json:"n"`
	Edges  []EdgeFit `json:"edges"`
	Before float64   `json:"loo_error_before"`
	After  float64   `json:"loo_error_after"`
	// Improvement is 1 - After/Before.
	Improvement float64 `json:"improvement"`
	YAML        string  `json:"yaml"`
	Why         string  `json:"why"`
}

// Report is the whole backtest.
type Report struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Decisions   int          `json:"decisions"`
	KPIs        []KPIFit     `json:"kpis"`
	Suggestions []Suggestion `json:"suggestions"`
	// ActionSuggestions correct an action's declared direct effect. They are
	// learned only from decisions that ran a single action, because a bundle
	// cannot say which of its actions moved a KPI.
	ActionSuggestions []ActionSuggestion `json:"action_suggestions"`
	// Notes say why nothing was suggested for a KPI that was examined.
	Notes []string `json:"notes,omitempty"`
	Note  string   `json:"note,omitempty"`
}

// sample is one decision's evidence about one KPI.
type sample struct {
	actualRel, predRel float64
	edges              map[string]float64 // incoming edge "from" -> predicted relative contribution
}

// Analyze backtests the model against finished, applied decisions.
func Analyze(m *graph.Model, proposals []approvals.Proposal, opt Options) Report {
	opt = opt.withDefaults()
	rep := Report{GeneratedAt: time.Now().UTC(), KPIs: []KPIFit{}, Suggestions: []Suggestion{}, ActionSuggestions: []ActionSuggestion{}}
	byKPI := map[string][]sample{}
	byAction := map[[2]string][]actionSample{}
	bundled := 0
	for _, p := range proposals {
		if !usable(p) {
			continue
		}
		rep.Decisions++
		single := len(p.Simulation.Actions) == 1
		if len(p.Simulation.Actions) > 1 {
			bundled++
		}
		for kpi, pred := range p.Predicted.KPIs {
			base, hasBase := p.Baseline[kpi]
			act, hasAct := p.Actual[kpi]
			if !hasBase || !hasAct || base == 0 {
				continue
			}
			s := sample{actualRel: (act - base) / math.Abs(base), predRel: (pred - base) / math.Abs(base), edges: map[string]float64{}}
			for _, st := range p.Simulation.Trace {
				if st.To == kpi && st.From != "" {
					s.edges[st.From] += st.Delta
				}
			}
			byKPI[kpi] = append(byKPI[kpi], s)
			if single {
				if d, ok := directRelative(p.Simulation, kpi); ok {
					key := [2]string{p.Simulation.Actions[0], kpi}
					byAction[key] = append(byAction[key], actionSample{actualRel: s.actualRel, predRel: s.predRel, direct: d})
				}
			}
		}
	}
	if rep.Decisions == 0 {
		rep.Note = "No applied decision has a finished outcome yet. Calibration learns from changes that actually ran; in dry-run mode nothing runs, so there is nothing to learn from."
		return rep
	}
	kpis := make([]string, 0, len(byKPI))
	for k := range byKPI {
		kpis = append(kpis, k)
	}
	sort.Strings(kpis)
	for _, kpi := range kpis {
		ss := byKPI[kpi]
		rep.KPIs = append(rep.KPIs, fit(kpi, ss))
		sug, note := suggest(m, kpi, ss, opt)
		if sug != nil {
			rep.Suggestions = append(rep.Suggestions, *sug)
		} else if note != "" {
			rep.Notes = append(rep.Notes, kpi+": "+note)
		}
	}
	keys := make([][2]string, 0, len(byAction))
	for k := range byAction {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		sug, note := suggestAction(m, k[0], k[1], byAction[k], opt)
		if sug != nil {
			rep.ActionSuggestions = append(rep.ActionSuggestions, *sug)
		} else if note != "" {
			rep.Notes = append(rep.Notes, "action "+k[0]+" on "+k[1]+": "+note)
		}
	}
	if bundled > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("%d decision(s) ran several actions together and are not used to correct an action's own effect; they still inform edge weights", bundled))
	}
	return rep
}

func usable(p approvals.Proposal) bool {
	if p.Status != approvals.Executed || p.Simulation == nil || len(p.Actual) == 0 || p.Outcome == nil || !p.Outcome.Done() {
		return false
	}
	switch p.Outcome.State {
	case "verified", "missed", "regressed":
		return true
	}
	return false
}

func fit(kpi string, ss []sample) KPIFit {
	f := KPIFit{KPI: kpi, N: len(ss)}
	hits := 0
	for _, s := range ss {
		f.MeanAbsError += math.Abs(s.actualRel - s.predRel)
		f.Bias += s.actualRel - s.predRel
		if s.predRel == 0 || s.actualRel*s.predRel > 0 {
			hits++
		}
	}
	n := float64(len(ss))
	f.MeanAbsError, f.Bias, f.HitRate = round(f.MeanAbsError/n), round(f.Bias/n), round(float64(hits)/n)
	return f
}

// suggest fits scales for a KPI's incoming edges. It returns nil and a reason
// when the data does not justify a suggestion.
func suggest(m *graph.Model, kpi string, ss []sample, opt Options) (*Suggestion, string) {
	// Only decisions in which an edge actually fed this KPI carry information.
	var used []sample
	edgeSet := map[string]bool{}
	for _, s := range ss {
		if len(s.edges) > 0 {
			used = append(used, s)
			for e := range s.edges {
				edgeSet[e] = true
			}
		}
	}
	if len(used) < opt.MinSamples {
		return nil, fmt.Sprintf("%d decision(s) moved it through an edge; %d are needed", len(used), opt.MinSamples)
	}
	froms := make([]string, 0, len(edgeSet))
	for e := range edgeSet {
		froms = append(froms, e)
	}
	sort.Strings(froms)
	// Targets: what the edges should have contributed once the direct part
	// of the prediction is taken out.
	y := make([]float64, len(used))
	c := make([][]float64, len(used))
	for i, s := range used {
		sum := 0.0
		c[i] = make([]float64, len(froms))
		for j, f := range froms {
			c[i][j] = s.edges[f]
			sum += s.edges[f]
		}
		y[i] = s.actualRel - (s.predRel - sum)
	}
	scales := ridge(c, y, nil, opt.PriorDecisions)
	var before, after float64
	for i := range used {
		var base float64
		for j := range froms {
			base += c[i][j]
		}
		before += math.Abs(y[i] - base)
		// Leave this decision out and predict it.
		loo := ridge(c, y, []int{i}, opt.PriorDecisions)
		var pred float64
		for j := range froms {
			pred += clamp(loo[j], opt.MinScale, opt.MaxScale) * c[i][j]
		}
		after += math.Abs(y[i] - pred)
	}
	n := float64(len(used))
	// Standard errors from the residual of the full fit, ignoring collinearity
	// (so they are, if anything, optimistic): an edge is only changed when its
	// scale is clearly away from 1.
	var rss float64
	for i := range used {
		pred := 0.0
		for j := range froms {
			pred += scales[j] * c[i][j]
		}
		rss += (y[i] - pred) * (y[i] - pred)
	}
	sigma := math.Sqrt(rss / math.Max(n-float64(len(froms)), 1))
	se := make([]float64, len(froms))
	for j := range froms {
		var a float64
		for i := range used {
			a += c[i][j] * c[i][j]
		}
		se[j] = math.Inf(1)
		if a > 0 {
			se[j] = sigma / math.Sqrt(a)
		}
	}
	before, after = before/n, after/n
	if before < 1e-9 {
		return nil, "the model already predicts these decisions"
	}
	improvement := 1 - after/before
	if improvement < opt.MinImprovement {
		return nil, fmt.Sprintf("a correction would improve held-out error by only %.0f%% (need %.0f%%)", improvement*100, opt.MinImprovement*100)
	}
	sug := &Suggestion{KPI: kpi, N: len(used), Before: round(before), After: round(after), Improvement: round(improvement)}
	var yaml []string
	for j, f := range froms {
		var e *graph.Edge
		for i := range m.Edges {
			if m.Edges[i].From == f && m.Edges[i].To == kpi {
				e = &m.Edges[i]
			}
		}
		if e == nil {
			continue // the trace names an edge the model no longer has
		}
		s := clamp(scales[j], opt.MinScale, opt.MaxScale)
		if math.Abs(s-1) < 0.05 || math.Abs(scales[j]-1) < 2*se[j] {
			continue // too small to matter, or not clearly different from the declared weight
		}
		ef := EdgeFit{From: f, To: kpi, Weight: e.Weight, Scale: round(s), Suggested: round3(e.Weight * s)}
		sug.Edges = append(sug.Edges, ef)
		line := fmt.Sprintf("  - { from: %s, to: %s, weight: %g", f, kpi, ef.Suggested)
		if e.Confidence > 0 {
			line += fmt.Sprintf(", confidence: %g", e.Confidence)
		}
		yaml = append(yaml, line+" } # was "+fmt.Sprint(e.Weight))
	}
	if len(sug.Edges) == 0 {
		return nil, "the fitted scales are within 5% of the declared weights"
	}
	sug.YAML = strings.Join(yaml, "\n")
	sug.Why = fmt.Sprintf("Across %d decisions the edges into %s moved it about %s as much as predicted; held-out error falls from %.3g to %.3g (%.0f%% better).",
		len(used), kpi, describe(sug.Edges), before, after, improvement*100)
	return sug, ""
}

func describe(es []EdgeFit) string {
	var parts []string
	for _, e := range es {
		parts = append(parts, fmt.Sprintf("%.2gx (%s)", e.Scale, e.From))
	}
	return strings.Join(parts, ", ")
}

// ridge solves for edge scales with a pull toward 1 worth prior decisions of
// each edge's average signal, so it fades as decisions accumulate. skip lists
// a row to leave out.
func ridge(c [][]float64, y []float64, skip []int, prior float64) []float64 {
	// (the prior is shared by all edges; see below)
	k := len(c[0])
	a := make([][]float64, k)
	b := make([]float64, k)
	for i := range a {
		a[i] = make([]float64, k)
	}
	rows := 0
	for r := range c {
		if len(skip) > 0 && skip[0] == r {
			continue
		}
		rows++
		for i := 0; i < k; i++ {
			b[i] += c[r][i] * y[r]
			for j := 0; j < k; j++ {
				a[i][j] += c[r][i] * c[r][j]
			}
		}
	}
	// One prior strength for every edge, set by the average signal: an edge
	// with little signal of its own is anchored to 1 instead of being fitted
	// to noise (or to the leakage of a stronger edge).
	var mean float64
	for i := 0; i < k; i++ {
		mean += a[i][i]
	}
	mean /= float64(k)
	pull := prior * mean / float64(max(rows, 1))
	if pull == 0 {
		pull = 1e-9
	}
	for i := 0; i < k; i++ {
		a[i][i] += pull
		b[i] += pull
	}
	return solve(a, b)
}

// solve is Gaussian elimination with partial pivoting; the systems here have
// one row per incoming edge of a KPI.
func solve(a [][]float64, b []float64) []float64 {
	n := len(b)
	for col := 0; col < n; col++ {
		p := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[p][col]) {
				p = r
			}
		}
		a[col], a[p] = a[p], a[col]
		b[col], b[p] = b[p], b[col]
		if a[col][col] == 0 {
			continue
		}
		for r := col + 1; r < n; r++ {
			f := a[r][col] / a[col][col]
			for k := col; k < n; k++ {
				a[r][k] -= f * a[col][k]
			}
			b[r] -= f * b[col]
		}
	}
	x := make([]float64, n)
	for i := n - 1; i >= 0; i-- {
		s := b[i]
		for j := i + 1; j < n; j++ {
			s -= a[i][j] * x[j]
		}
		if a[i][i] == 0 {
			x[i] = 1
		} else {
			x[i] = s / a[i][i]
		}
	}
	return x
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
func round(v float64) float64         { return math.Round(v*1000) / 1000 }
func round3(v float64) float64        { return math.Round(v*10000) / 10000 }

var _ = sim.Step{} // the trace type this package reads
