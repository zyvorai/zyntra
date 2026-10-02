// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package review looks back at decisions: why a prediction missed, which
// past decisions resemble a new one, which KPIs move together without an
// edge, and which scored actions break a pack's own rules. Everything here
// is computed from the decision record, the model and the KPI history. A
// model may reword the result; it never supplies a number.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/outcome"
	"github.com/zyvorai/zyntra/internal/sim"
)

// rel is the relative change from base to v (absolute when base is 0).
func rel(base, v float64) float64 {
	if base == 0 {
		return v
	}
	return (v - base) / math.Abs(base)
}

func pct(v float64) string { return fmt.Sprintf("%+.1f%%", v*100) }

// step returns the trace step that moved kpi the most.
func step(r *sim.Result, kpi string) (sim.Step, bool) {
	var best sim.Step
	found := false
	if r == nil {
		return best, false
	}
	for _, s := range r.Trace {
		if s.To == kpi && (!found || math.Abs(s.Delta) > math.Abs(best.Delta)) {
			best, found = s, true
		}
	}
	return best, found
}

// Explain compares predicted and actual for a decided proposal. It uses
// only the outcome record, the proposal's simulation and its inputs.
func Explain(p approvals.Proposal) (outcome.Explanation, bool) {
	o := p.Outcome
	if o == nil || !o.Done() {
		return outcome.Explanation{}, false
	}
	ex := outcome.Explanation{Proposal: p.ID, Verdict: o.State, Grounding: []string{"proposal:" + p.ID}}
	if hr, ok := o.HitRate(); ok {
		ex.HitRate = &hr
	}
	acc := map[string]outcome.Accuracy{}
	for _, a := range o.Accuracy {
		acc[a.KPI] = a
	}
	missed := func(id string) bool { a, ok := acc[id]; return ok && !a.Hit }

	for _, a := range o.Accuracy {
		ex.Grounding = append(ex.Grounding, "accuracy:"+a.KPI)
		f := outcome.Finding{KPI: a.KPI, PredictedChange: rel(a.Baseline, a.Predicted), ActualChange: rel(a.Baseline, a.Actual)}
		st, traced := step(p.Simulation, a.KPI)
		if traced && st.From != "" {
			f.Edge, f.Weight = st.From+"->"+st.To, st.Weight
		}
		switch {
		case a.Hit:
			f.Kind = "hit"
			f.Text = fmt.Sprintf("%s landed: predicted %s, observed %s.", a.KPI, pct(f.PredictedChange), pct(f.ActualChange))
		case f.PredictedChange != 0 && math.Abs(f.ActualChange) > 0.01 && math.Signbit(f.PredictedChange) != math.Signbit(f.ActualChange):
			f.Kind = "wrong-direction"
			f.Text = fmt.Sprintf("%s moved the wrong way: predicted %s, observed %s.", a.KPI, pct(f.PredictedChange), pct(f.ActualChange))
			if f.Edge != "" {
				f.Text += fmt.Sprintf(" The change came through edge %s (weight %+.3g); its sign is in doubt.", f.Edge, f.Weight)
			}
		case f.Edge != "" && missed(st.From):
			f.Kind = "inherited"
			up := acc[st.From]
			f.Text = fmt.Sprintf("%s missed (predicted %s, observed %s) because upstream %s missed first (predicted %s, observed %s); edge %s only carried it.",
				a.KPI, pct(f.PredictedChange), pct(f.ActualChange), st.From, pct(rel(up.Baseline, up.Predicted)), pct(rel(up.Baseline, up.Actual)), f.Edge)
		default:
			over := math.Abs(f.ActualChange) < math.Abs(f.PredictedChange)
			ratio := 0.0
			if f.PredictedChange != 0 {
				ratio = f.ActualChange / f.PredictedChange
			}
			word := "undershot"
			if over {
				word = "overshot"
			}
			if f.Edge != "" {
				f.Kind = "edge-" + word
				f.Text = fmt.Sprintf("Edge %s (weight %+.3g) %s: %s was predicted %s and moved %s.", f.Edge, f.Weight, word, a.KPI, pct(f.PredictedChange), pct(f.ActualChange))
				if ratio != 0 && f.Weight != 0 {
					w := math.Round(f.Weight*ratio*1000) / 1000
					f.SuggestedWeight = &w
					f.Text += fmt.Sprintf(" A weight near %+.3g fits this one run; one run is not enough to change it.", w)
				}
			} else {
				f.Kind = "effect-" + word
				f.Text = fmt.Sprintf("The action's direct effect on %s %s: predicted %s, observed %s.", a.KPI, word, pct(f.PredictedChange), pct(f.ActualChange))
			}
		}
		ex.Findings = append(ex.Findings, f)
	}

	staleRuns := map[string]int{}
	for _, s := range o.Samples {
		for _, id := range s.Stale {
			staleRuns[id]++
		}
	}
	for _, id := range sortedKeys(staleRuns) {
		ex.Findings = append(ex.Findings, outcome.Finding{KPI: id, Kind: "stale-input",
			Text: fmt.Sprintf("%s was stale in %d of %d samples; its actual is the last fresh value.", id, staleRuns[id], len(o.Samples))})
		ex.Grounding = append(ex.Grounding, "sample-stale:"+id)
	}
	if p.Inputs != nil {
		usable := map[string]freshness.State{}
		for _, st := range p.Inputs.Freshness {
			usable[st.KPI] = st
		}
		if p.Simulation != nil {
			for _, f := range p.Simulation.PreconditionFailures {
				ex.Findings = append(ex.Findings, outcome.Finding{Kind: "stale-precondition", Text: "At proposal time a precondition did not hold: " + f})
			}
		}
		for _, st := range p.Inputs.Freshness {
			if _, predicted := o.Predicted[st.KPI]; predicted && !st.Usable() {
				ex.Findings = append(ex.Findings, outcome.Finding{KPI: st.KPI, Kind: "stale-precondition",
					Text: fmt.Sprintf("%s was %s when the decision was made, so its baseline was not current.", st.KPI, st.Status)})
				ex.Grounding = append(ex.Grounding, "freshness:"+st.KPI)
			}
		}
		for _, src := range p.Inputs.Sources {
			if src.State != "fallback" && src.State != "error" && src.State != "stale" {
				continue
			}
			var hit []string
			for _, k := range src.KPIs {
				if _, ok := o.Predicted[k]; ok {
					hit = append(hit, k)
				}
			}
			if len(hit) > 0 {
				sort.Strings(hit)
				ex.Findings = append(ex.Findings, outcome.Finding{KPI: hit[0], Kind: "fallback-source",
					Text: fmt.Sprintf("Source %s was %s at decision time; %s used its last or model value.", src.Name, src.State, strings.Join(hit, ", "))})
				ex.Grounding = append(ex.Grounding, "source:"+src.Name)
			}
		}
	}

	ex.Text = summary(ex)
	ex.Hash = hashOf(ex)
	return ex, true
}

func summary(ex outcome.Explanation) string {
	var miss, notes []string
	hits := 0
	for _, f := range ex.Findings {
		switch f.Kind {
		case "hit":
			hits++
		case "stale-input", "stale-precondition", "fallback-source":
			notes = append(notes, f.Text)
		default:
			miss = append(miss, f.Text)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Outcome %s.", ex.Verdict)
	if ex.HitRate != nil {
		fmt.Fprintf(&b, " %d of %d predicted KPIs landed.", hits, hits+len(miss))
	}
	for _, m := range miss {
		b.WriteString(" " + m)
	}
	for _, n := range notes {
		b.WriteString(" " + n)
	}
	if len(miss) == 0 && len(notes) == 0 {
		b.WriteString(" Every predicted KPI landed within tolerance.")
	}
	return b.String()
}

func hashOf(ex outcome.Explanation) string {
	ex.Hash = ""
	b, _ := json.Marshal(ex)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// VerifyHash reports whether an explanation still matches its hash.
func VerifyHash(ex outcome.Explanation) bool { return ex.Hash != "" && hashOf(ex) == ex.Hash }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// edgeIndex answers "is there a path between a and b" for the model.
func edgeIndex(m *graph.Model) func(a, b string) bool {
	out := map[string][]string{}
	for _, e := range m.Edges {
		out[e.From] = append(out[e.From], e.To)
	}
	reach := func(a, b string) bool {
		seen := map[string]bool{a: true}
		q := []string{a}
		for len(q) > 0 {
			n := q[0]
			q = q[1:]
			for _, x := range out[n] {
				if x == b {
					return true
				}
				if !seen[x] {
					seen[x] = true
					q = append(q, x)
				}
			}
		}
		return false
	}
	return func(a, b string) bool { return reach(a, b) || reach(b, a) }
}
