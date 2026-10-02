// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package outcome watches KPIs after an action is applied and decides
// whether the action did what it promised: verified when the success
// criteria hold for enough consecutive fresh samples, regressed when a
// guardrail KPI gets worse, missed when the window ends without success and
// inconclusive when the data needed to judge was stale.
package outcome

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

type State string

const (
	Observing    State = "observing"
	Verified     State = "verified"
	Regressed    State = "regressed"
	Missed       State = "missed"
	Inconclusive State = "inconclusive"
)

const (
	DefaultWindow    = 15 * time.Minute
	DefaultSamples   = 2
	DefaultTolerance = 0.05
	maxSamples       = 100
)

// Spec is what to observe for one decision.
type Spec struct {
	Window     time.Duration
	Samples    int
	Success    []graph.Criterion
	Guardrails []string
	Tolerance  float64
	// Predicted holds the simulated after-values scored for accuracy.
	Predicted map[string]float64
}

// SpecFor builds the observation spec for actions taken together. Explicit
// outcome blocks win; otherwise success means every predicted KPI with a
// target meets it (or, without targets, moves the healthy way), and the
// guardrails are the critical and constrained KPIs.
func SpecFor(m *graph.Model, actions []graph.Action, baseline, predicted map[string]float64) Spec {
	s := Spec{Tolerance: -1, Predicted: map[string]float64{}}
	maps.Copy(s.Predicted, predicted)
	guard := map[string]bool{}
	for _, a := range actions {
		o := a.Outcome
		if o == nil {
			continue
		}
		s.Window = max(s.Window, o.Window.D())
		s.Samples = max(s.Samples, o.Samples)
		s.Success = append(s.Success, o.Success...)
		for _, g := range o.Guardrails {
			guard[g] = true
		}
		if o.Tolerance > 0 && (s.Tolerance < 0 || o.Tolerance < s.Tolerance) {
			s.Tolerance = o.Tolerance
		}
	}
	if s.Window <= 0 {
		s.Window = DefaultWindow
	}
	if s.Samples <= 0 {
		s.Samples = DefaultSamples
	}
	if s.Tolerance < 0 {
		s.Tolerance = DefaultTolerance
	}
	if len(s.Success) == 0 {
		s.Success = defaultSuccess(m, baseline, predicted)
	}
	if len(guard) == 0 {
		for _, k := range m.KPIs {
			if k.Criticality == graph.CriticalityCritical {
				guard[k.ID] = true
			}
		}
		for _, c := range m.AllConstraints() {
			guard[c.KPI] = true
		}
	}
	for _, c := range s.Success {
		delete(guard, c.KPI)
	}
	for g := range guard {
		s.Guardrails = append(s.Guardrails, g)
	}
	sort.Strings(s.Guardrails)
	return s
}

func defaultSuccess(m *graph.Model, baseline, predicted map[string]float64) []graph.Criterion {
	ids := make([]string, 0, len(predicted))
	for id := range predicted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var met, moved []graph.Criterion
	for _, id := range ids {
		k, ok := m.KPI(id)
		if !ok {
			continue
		}
		if k.Target != nil && k.Meets(predicted[id]) {
			met = append(met, graph.Criterion{KPI: id, Op: "met"})
			continue
		}
		b, ok := baseline[id]
		if !ok || predicted[id] == b || k.Worse(b, predicted[id]) {
			continue
		}
		v := b
		switch k.Direction {
		case graph.HigherIsBetter:
			moved = append(moved, graph.Criterion{KPI: id, Op: ">", Value: &v})
		case graph.LowerIsBetter:
			moved = append(moved, graph.Criterion{KPI: id, Op: "<", Value: &v})
		}
	}
	if len(met) > 0 {
		return met
	}
	return moved
}

// Sample is one observation.
type Sample struct {
	At       time.Time          `json:"at"`
	Values   map[string]float64 `json:"values"`
	Stale    []string           `json:"stale,omitempty"`
	Met      bool               `json:"met"`
	Breaches []string           `json:"breaches,omitempty"`
}

// Record is the persisted observation of one decision.
type Record struct {
	State      State              `json:"state"`
	StartedAt  time.Time          `json:"started_at"`
	Until      time.Time          `json:"until"`
	Window     string             `json:"window"`
	Required   int                `json:"required_samples"`
	Tolerance  float64            `json:"tolerance"`
	Success    []graph.Criterion  `json:"success_criteria"`
	Guardrails []string           `json:"guardrails,omitempty"`
	Baseline   map[string]float64 `json:"baseline"`
	Samples    []Sample           `json:"samples"`
	Reasons    []string           `json:"reasons,omitempty"`
	DecidedAt  *time.Time         `json:"decided_at,omitempty"`
	Predicted  map[string]float64 `json:"predicted,omitempty"`
	// Accuracy scores each prediction against the last fresh value seen.
	Accuracy []Accuracy `json:"accuracy,omitempty"`
}

// Accuracy compares one predicted KPI with what happened. A hit moved the
// predicted way and landed within half of the predicted change of the
// prediction.
type Accuracy struct {
	KPI       string  `json:"kpi"`
	Baseline  float64 `json:"baseline"`
	Predicted float64 `json:"predicted"`
	Actual    float64 `json:"actual"`
	AbsError  float64 `json:"abs_error"`
	Hit       bool    `json:"hit"`
}

// Score returns the accuracy of predicted against actual from baseline.
func Score(kpi string, baseline, predicted, actual float64) Accuracy {
	a := Accuracy{KPI: kpi, Baseline: baseline, Predicted: predicted, Actual: actual, AbsError: math.Abs(actual - predicted)}
	want, got := predicted-baseline, actual-baseline
	if want == 0 {
		a.Hit = a.AbsError <= 1e-9*(1+math.Abs(baseline))
		return a
	}
	a.Hit = math.Signbit(want) == math.Signbit(got) && got != 0 && a.AbsError <= 0.5*math.Abs(want)
	return a
}

// HitRate is the fraction of scored KPIs that were hits.
func (r *Record) HitRate() (float64, bool) {
	if len(r.Accuracy) == 0 {
		return 0, false
	}
	n := 0
	for _, a := range r.Accuracy {
		if a.Hit {
			n++
		}
	}
	return float64(n) / float64(len(r.Accuracy)), true
}

// Start opens an observation. baseline holds KPI values from before the
// action ran.
func Start(s Spec, baseline map[string]float64, at time.Time) *Record {
	b := map[string]float64{}
	maps.Copy(b, baseline)
	p := map[string]float64{}
	maps.Copy(p, s.Predicted)
	return &Record{
		State: Observing, StartedAt: at, Until: at.Add(s.Window), Window: s.Window.String(),
		Required: s.Samples, Tolerance: s.Tolerance, Success: s.Success, Guardrails: s.Guardrails,
		Baseline: b, Samples: []Sample{}, Predicted: p,
	}
}

// Done reports whether the observation reached a verdict.
func (r *Record) Done() bool { return r.State != Observing }

func (r *Record) finish(st State, at time.Time, reasons ...string) {
	r.State, r.Reasons = st, reasons
	r.DecidedAt = &at
	r.Accuracy = nil
	ids := make([]string, 0, len(r.Predicted))
	for id := range r.Predicted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		actual, ok := r.lastFresh(id)
		b, has := r.Baseline[id]
		if !ok || !has {
			continue
		}
		r.Accuracy = append(r.Accuracy, Score(id, b, r.Predicted[id], actual))
	}
}

func (r *Record) lastFresh(id string) (float64, bool) {
	for _, v := range slices.Backward(r.Samples) {
		if v, ok := v.Values[id]; ok {
			return v, true
		}
	}
	return 0, false
}

// Observe records a sample from m. unusable lists KPIs whose current value
// is stale or missing. It returns true when the observation reached a
// verdict.
func (r *Record) Observe(m *graph.Model, unusable map[string]bool, at time.Time) bool {
	if r.Done() {
		return false
	}
	smp := Sample{At: at, Values: map[string]float64{}, Met: true}
	stale := func(id string) bool {
		k, ok := m.KPI(id)
		if !ok || unusable[id] {
			smp.Stale = append(smp.Stale, id)
			return true
		}
		smp.Values[id] = k.Value
		return false
	}
	for id := range r.Predicted {
		if k, ok := m.KPI(id); ok && !unusable[id] {
			smp.Values[id] = k.Value
		}
	}
	for _, c := range r.Success {
		if stale(c.KPI) {
			smp.Met = false
			continue
		}
		k, _ := m.KPI(c.KPI)
		if !c.Holds(*k, k.Value) {
			smp.Met = false
		}
	}
	for _, g := range r.Guardrails {
		if stale(g) {
			continue
		}
		k, _ := m.KPI(g)
		b, ok := r.Baseline[g]
		if !ok || !k.Worse(b, k.Value) {
			continue
		}
		if rel := math.Abs(k.Value-b) / math.Max(math.Abs(b), 1e-9); rel > r.Tolerance {
			smp.Breaches = append(smp.Breaches, fmt.Sprintf("%s worsened %.4g -> %.4g (%.1f%%, tolerance %.1f%%)", g, b, k.Value, rel*100, r.Tolerance*100))
		}
	}
	for _, c := range m.AllConstraints() {
		k, ok := m.KPI(c.KPI)
		b, has := r.Baseline[c.KPI]
		if !ok || !has || unusable[c.KPI] {
			continue
		}
		if v, broken := c.Check(*k, b, k.Value); broken {
			smp.Breaches = append(smp.Breaches, "constraint: "+v.Text)
		}
	}
	sort.Strings(smp.Stale)
	smp.Stale = uniq(smp.Stale)
	if len(r.Success) == 0 {
		smp.Met = len(smp.Stale) == 0
	}
	r.Samples = append(r.Samples, smp)
	if n := len(r.Samples); n > maxSamples {
		r.Samples = r.Samples[n-maxSamples:]
	}
	if len(smp.Breaches) > 0 {
		r.finish(Regressed, at, smp.Breaches...)
		return true
	}
	run := 0
	for i := len(r.Samples) - 1; i >= 0 && r.Samples[i].Met; i-- {
		run++
	}
	if run >= r.Required {
		r.finish(Verified, at, fmt.Sprintf("success criteria held for %d consecutive fresh samples", run))
		return true
	}
	if !at.Before(r.Until) {
		if len(smp.Stale) > 0 {
			r.finish(Inconclusive, at, "inputs stale at the end of the window: "+join(smp.Stale))
		} else {
			r.finish(Missed, at, fmt.Sprintf("success criteria not met for %d consecutive samples within %s", r.Required, r.Window))
		}
		return true
	}
	return false
}

func uniq(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

func join(s []string) string {
	var out strings.Builder
	for i, x := range s {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(x)
	}
	return out.String()
}
