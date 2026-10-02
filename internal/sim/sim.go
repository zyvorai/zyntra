// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package sim runs deterministic what-if simulations over the KPI graph.
// Every predicted change is traceable to an action effect, an edge, a bound
// or a saturation step.
//
// Each KPI carries a nominal relative change plus a low/high band. Direct
// effects add relative changes (a fraction of the current value) or absolute
// amounts (in the KPI's unit). Edges propagate the KPI's effective relative
// change in topological order; for a KPI starting at zero the effective
// relative change is measured against 1 unit. Results are clamped to the
// KPI's [min, max]; without a min, a non-negative KPI cannot go below zero.
package sim

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
)

type KPIResult struct {
	KPI         string  `json:"kpi"`
	Name        string  `json:"name"`
	Unit        string  `json:"unit,omitempty"`
	Before      float64 `json:"before"`
	After       float64 `json:"after"`
	Low         float64 `json:"low"`
	High        float64 `json:"high"`
	Change      float64 `json:"change"`
	MetBefore   bool    `json:"met_before"`
	MetAfter    bool    `json:"met_after"`
	HasTarget   bool    `json:"has_target"`
	SeverityOld float64 `json:"severity_before"`
	SeverityNew float64 `json:"severity_after"`
	// SettlesAfter is the longest delay along the paths that move this KPI.
	SettlesAfter graph.Duration `json:"settles_after,omitempty"`
	Bounded      bool           `json:"bounded,omitempty"`
	Stale        bool           `json:"stale,omitempty"`
}

// Step is one link in the explanation trace.
type Step struct {
	From   string         `json:"from,omitempty"`
	To     string         `json:"to"`
	Weight float64        `json:"weight,omitempty"`
	Delta  float64        `json:"delta"`
	Delay  graph.Duration `json:"delay,omitempty"`
	Text   string         `json:"text"`
}

type Result struct {
	// Action is the action id, or ids joined with "+" for a plan.
	Action     string      `json:"action"`
	ActionName string      `json:"action_name"`
	Actions    []string    `json:"actions"`
	KPIs       []KPIResult `json:"kpis"`
	Trace      []Step      `json:"trace"`
	// SeverityBefore/After are unweighted totals (kept for compatibility).
	SeverityBefore float64 `json:"severity_before"`
	SeverityAfter  float64 `json:"severity_after"`
	// Weighted totals multiply each KPI's severity by its criticality.
	WeightedBefore float64 `json:"weighted_before"`
	WeightedAfter  float64 `json:"weighted_after"`
	// WeightedAfterBest/Worst use the optimistic and pessimistic ends of
	// every KPI's band.
	WeightedAfterBest  float64           `json:"weighted_after_best"`
	WeightedAfterWorst float64           `json:"weighted_after_worst"`
	GapsClosed         []string          `json:"gaps_closed"`
	GapsOpened         []string          `json:"gaps_opened"`
	Violations         []graph.Violation `json:"violations,omitempty"`
	// PreconditionFailures say why the action cannot be approved right now.
	// Preconditions fail closed: a stale input fails its precondition.
	PreconditionFailures []string `json:"precondition_failures,omitempty"`
	// StaleInputs are stale or missing KPIs that this prediction depends on.
	StaleInputs  []string       `json:"stale_inputs,omitempty"`
	SettlesAfter graph.Duration `json:"settles_after,omitempty"`
}

// Improvement is the reduction in total gap severity. Negative means the
// action makes things worse overall.
func (r Result) Improvement() float64 { return r.SeverityBefore - r.SeverityAfter }

// WeightedImprovement is the reduction in criticality-weighted severity.
func (r Result) WeightedImprovement() float64 { return r.WeightedBefore - r.WeightedAfter }

// Uncertainty is the width of the weighted-severity band after the action.
func (r Result) Uncertainty() float64 { return r.WeightedAfterWorst - r.WeightedAfterBest }

// Options tune a simulation.
type Options struct {
	// Unusable marks KPIs whose current value is stale or missing.
	Unusable map[string]bool
}

// Simulate applies an action's direct effects and propagates relative changes
// along edges in topological order. The model is not modified.
func Simulate(m *graph.Model, actionID string) (Result, error) {
	a, ok := m.Action(actionID)
	if !ok {
		return Result{}, fmt.Errorf("unknown action %q", actionID)
	}
	return Apply(m, *a)
}

// Apply simulates a single action.
func Apply(m *graph.Model, a graph.Action) (Result, error) {
	return ApplyPlan(m, []graph.Action{a}, Options{})
}

// interval is a [lo, hi] range around a nominal value.
type interval struct{ lo, nom, hi float64 }

func (a interval) add(b interval) interval {
	return interval{a.lo + b.lo, a.nom + b.nom, a.hi + b.hi}
}

func (a interval) zero() bool { return a.lo == 0 && a.nom == 0 && a.hi == 0 }

// band returns x widened by +/- frac, ordered.
func band(x, frac float64) interval {
	lo, hi := x*(1-frac), x*(1+frac)
	if lo > hi {
		lo, hi = hi, lo
	}
	return interval{lo, x, hi}
}

// mul multiplies two intervals.
func mul(a, b interval) interval {
	p := []float64{a.lo * b.lo, a.lo * b.hi, a.hi * b.lo, a.hi * b.hi}
	lo, hi := p[0], p[0]
	for _, v := range p[1:] {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return interval{lo, a.nom * b.nom, hi}
}

func saturate(x, limit float64) float64 {
	if limit <= 0 || x == 0 {
		return x
	}
	return math.Copysign(limit*(1-math.Exp(-math.Abs(x)/limit)), x)
}

func (a interval) saturate(limit float64) interval {
	return interval{saturate(a.lo, limit), saturate(a.nom, limit), saturate(a.hi, limit)}
}

func denom(before float64) float64 {
	if before == 0 {
		return 1
	}
	return math.Abs(before)
}

// ApplyPlan simulates one or more actions together; their effects add up.
func ApplyPlan(m *graph.Model, actions []graph.Action, opt Options) (Result, error) {
	if len(actions) == 0 {
		return Result{}, fmt.Errorf("no actions to simulate")
	}
	order, err := m.TopoOrder()
	if err != nil {
		return Result{}, err
	}
	var res Result
	var names []string
	for _, a := range actions {
		res.Actions = append(res.Actions, a.ID)
		n := a.Name
		if n == "" {
			n = a.ID
		}
		names = append(names, n)
	}
	res.Action, res.ActionName = strings.Join(res.Actions, "+"), strings.Join(names, " + ")

	rel := map[string]interval{}
	abs := map[string]interval{}
	settle := map[string]time.Duration{}
	for _, a := range actions {
		for _, ef := range a.Effects {
			if _, ok := m.KPI(ef.KPI); !ok {
				return Result{}, fmt.Errorf("action %q: unknown kpi %q", a.ID, ef.KPI)
			}
			iv := band(ef.Change, ef.Uncertainty).saturate(ef.Saturation)
			text := fmt.Sprintf("%s changes %s by %s (direct effect)", a.ID, ef.KPI, Pct(ef.Change))
			if ef.Mode == graph.EffectAbsolute {
				abs[ef.KPI] = abs[ef.KPI].add(iv)
				text = fmt.Sprintf("%s changes %s by %+.4g (direct effect, absolute)", a.ID, ef.KPI, ef.Change)
			} else {
				rel[ef.KPI] = rel[ef.KPI].add(iv)
			}
			if ef.Saturation > 0 && iv.nom != ef.Change {
				text += fmt.Sprintf("; saturates toward %.4g, effective %.4g", ef.Saturation, iv.nom)
			}
			if ef.Uncertainty > 0 {
				text += fmt.Sprintf(" ±%.0f%%", ef.Uncertainty*100)
			}
			if ef.Delay > 0 {
				text += fmt.Sprintf(", after %s", ef.Delay.D())
				if ef.Delay.D() > settle[ef.KPI] {
					settle[ef.KPI] = ef.Delay.D()
				}
			}
			res.Trace = append(res.Trace, Step{To: ef.KPI, Delta: iv.nom, Delay: ef.Delay, Text: text})
		}
	}

	in := make(map[string][]graph.Edge)
	for _, e := range m.Edges {
		in[e.To] = append(in[e.To], e)
	}
	eff := map[string]interval{} // effective relative change seen downstream
	after := map[string]interval{}
	bounded := map[string]bool{}
	for _, id := range order {
		k, _ := m.KPI(id)
		r := rel[id]
		for _, e := range in[id] {
			d := eff[e.From]
			if d.zero() || e.Weight == 0 {
				continue
			}
			contrib := mul(band(e.Weight, e.Confidence), d)
			r = r.add(contrib)
			text := fmt.Sprintf("%s %s -> %s %s (weight %+.3g", e.From, Pct(d.nom), id, Pct(contrib.nom), e.Weight)
			if e.Confidence > 0 {
				text += fmt.Sprintf(" ±%.0f%%", e.Confidence*100)
			}
			text += ")"
			if e.Delay > 0 {
				text += fmt.Sprintf(" after %s", e.Delay.D())
			}
			if e.Provenance == graph.ProvenanceLearned {
				text += " [learned, assumed relationship]"
			}
			if e.Why != "" {
				text += ": " + e.Why
			}
			if s := settle[e.From] + e.Delay.D(); s > settle[id] {
				settle[id] = s
			}
			res.Trace = append(res.Trace, Step{From: e.From, To: id, Weight: e.Weight, Delta: contrib.nom, Delay: e.Delay, Text: text})
		}
		before := k.Value
		value := func(x, a float64) float64 { return before*(1+x) + a }
		ab := abs[id]
		ends := []float64{value(r.lo, ab.lo), value(r.lo, ab.hi), value(r.hi, ab.lo), value(r.hi, ab.hi)}
		lo, hi := ends[0], ends[0]
		for _, v := range ends[1:] {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
		out := interval{lo, value(r.nom, ab.nom), hi}
		clamp := func(v float64) float64 {
			c := k.Clamp(v)
			if k.Min == nil && before >= 0 && c < 0 {
				c = 0
			}
			return c
		}
		cl := interval{clamp(out.lo), clamp(out.nom), clamp(out.hi)}
		if cl.nom != out.nom {
			bounded[id] = true
			res.Trace = append(res.Trace, Step{To: id, Delta: (cl.nom - before) / denom(before),
				Text: fmt.Sprintf("%s held at %.4g by its bounds (unbounded prediction %.4g)", id, cl.nom, out.nom)})
		}
		after[id] = cl
		dn := denom(before)
		eff[id] = interval{(cl.lo - before) / dn, (cl.nom - before) / dn, (cl.hi - before) / dn}
	}

	nominal := make(map[string]float64, len(m.KPIs))
	best := make(map[string]float64, len(m.KPIs))
	worst := make(map[string]float64, len(m.KPIs))
	var stale []string
	var maxSettle time.Duration
	for _, k := range m.KPIs {
		a := after[k.ID]
		nominal[k.ID] = a.nom
		b, w := a.hi, a.lo
		if k.Direction == graph.LowerIsBetter {
			b, w = a.lo, a.hi
		}
		best[k.ID], worst[k.ID] = b, w
		sb, sa := gaps.Severity(k, k.Value), gaps.Severity(k, a.nom)
		touched := !eff[k.ID].zero() || !rel[k.ID].zero() || !abs[k.ID].zero()
		kr := KPIResult{
			KPI: k.ID, Name: k.Name, Unit: k.Unit,
			Before: k.Value, After: a.nom, Low: a.lo, High: a.hi, Change: eff[k.ID].nom,
			HasTarget: k.Target != nil, MetBefore: sb == 0, MetAfter: sa == 0,
			SeverityOld: sb, SeverityNew: sa, Bounded: bounded[k.ID],
		}
		if touched {
			kr.SettlesAfter = graph.Duration(settle[k.ID])
			if settle[k.ID] > maxSettle {
				maxSettle = settle[k.ID]
			}
			if opt.Unusable[k.ID] {
				kr.Stale = true
				stale = append(stale, k.ID)
			}
		}
		res.KPIs = append(res.KPIs, kr)
		if kr.HasTarget && !kr.MetBefore && kr.MetAfter {
			res.GapsClosed = append(res.GapsClosed, k.ID)
		}
		if kr.HasTarget && kr.MetBefore && !kr.MetAfter {
			res.GapsOpened = append(res.GapsOpened, k.ID)
		}
	}
	sort.Strings(stale)
	res.StaleInputs = stale
	res.SettlesAfter = graph.Duration(maxSettle)
	res.SeverityBefore = gaps.Total(m, nil)
	res.SeverityAfter = gaps.Total(m, nominal)
	res.WeightedBefore = gaps.WeightedTotal(m, nil)
	res.WeightedAfter = gaps.WeightedTotal(m, nominal)
	res.WeightedAfterBest = gaps.WeightedTotal(m, best)
	res.WeightedAfterWorst = gaps.WeightedTotal(m, worst)

	for _, c := range m.AllConstraints() {
		k, _ := m.KPI(c.KPI)
		if v, bad := c.Check(*k, k.Value, nominal[c.KPI]); bad {
			res.Violations = append(res.Violations, v)
			continue
		}
		a := after[c.KPI]
		for _, x := range []float64{a.lo, a.hi} {
			if v, bad := c.Check(*k, k.Value, x); bad {
				v.Text += " in the pessimistic case"
				res.Violations = append(res.Violations, v)
				break
			}
		}
	}
	for _, a := range actions {
		for _, iv := range a.Invariants {
			k, _ := m.KPI(iv.KPI)
			if v, bad := invariant(a.ID, iv, *k, nominal[iv.KPI]); bad {
				res.Violations = append(res.Violations, v)
			} else if v, bad := invariant(a.ID, iv, *k, worst[iv.KPI]); bad {
				v.Text += " in the pessimistic case"
				res.Violations = append(res.Violations, v)
			}
		}
		for _, p := range a.Preconditions {
			k, _ := m.KPI(p.KPI)
			switch {
			case opt.Unusable[p.KPI]:
				res.PreconditionFailures = append(res.PreconditionFailures,
					fmt.Sprintf("%s: precondition on %s cannot be checked because the input is stale", a.ID, p.KPI))
			case !p.Holds(*k, k.Value):
				res.PreconditionFailures = append(res.PreconditionFailures,
					fmt.Sprintf("%s: precondition not met: %s (now %.4g)", a.ID, p.Text(), k.Value))
			}
		}
	}
	return res, nil
}

// invariant checks that moving k to after worsens it by no more than the
// invariant allows.
func invariant(action string, iv graph.Invariant, k graph.KPI, after float64) (graph.Violation, bool) {
	before := k.Value
	if !k.Worse(before, after) {
		return graph.Violation{}, false
	}
	rel := math.Abs(after-before) / math.Max(math.Abs(before), 1e-9)
	if rel <= iv.MaxWorsen+1e-9 {
		return graph.Violation{}, false
	}
	why := ""
	if iv.Why != "" {
		why = " (" + iv.Why + ")"
	}
	return graph.Violation{
		KPI: iv.KPI, Value: after, Limit: iv.MaxWorsen, Kind: "invariant",
		Text: fmt.Sprintf("%s would worsen %s by %.1f%% (%.4g -> %.4g), more than its %.1f%% invariant%s",
			action, iv.KPI, rel*100, before, after, iv.MaxWorsen*100, why),
	}, true
}

// Pct formats a relative change, keeping precision for sub-1% moves.
func Pct(x float64) string {
	if x != 0 && math.Abs(x) < 0.01 {
		return fmt.Sprintf("%+.2f%%", x*100)
	}
	return fmt.Sprintf("%+.1f%%", x*100)
}
