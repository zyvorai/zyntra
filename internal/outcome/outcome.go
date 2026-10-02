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
	"math"
	"sort"
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
}

// SpecFor builds the observation spec for actions taken together. Explicit
// outcome blocks win; otherwise success means every predicted KPI with a
// target meets it (or, without targets, moves the healthy way), and the
// guardrails are the critical and constrained KPIs.
func SpecFor(m *graph.Model, actions []graph.Action, baseline, predicted map[string]float64) Spec {
	s := Spec{Tolerance: -1}
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
}

// Start opens an observation. baseline holds KPI values from before the
// action ran.
func Start(s Spec, baseline map[string]float64, at time.Time) *Record {
	b := map[string]float64{}
	for k, v := range baseline {
		b[k] = v
	}
	return &Record{
		State: Observing, StartedAt: at, Until: at.Add(s.Window), Window: s.Window.String(),
		Required: s.Samples, Tolerance: s.Tolerance, Success: s.Success, Guardrails: s.Guardrails,
		Baseline: b, Samples: []Sample{},
	}
}

// Done reports whether the observation reached a verdict.
func (r *Record) Done() bool { return r.State != Observing }

func (r *Record) finish(st State, at time.Time, reasons ...string) {
	r.State, r.Reasons = st, reasons
	r.DecidedAt = &at
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
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
