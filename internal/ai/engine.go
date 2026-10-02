// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
)

// Snapshot is everything an answer may draw on.
type Snapshot struct {
	Model     *graph.Model             `json:"-"`
	Gaps      []gaps.Gap               `json:"gaps"`
	Severity  float64                  `json:"severity_total"`
	Plan      []planner.Recommendation `json:"-"`
	Anomalies []Anomaly                `json:"anomalies"`
	Forecasts []Forecast               `json:"forecasts"`
	Sources   []adapters.Status        `json:"sources"`
	// Objects, when set, lets Ask answer about business objects. Its reader
	// is already scoped to the asking principal.
	Objects *ObjectContext `json:"-"`
	// ObjectsOnly confines Ask to business objects: a tenant-bound caller
	// gets no KPI, plan, forecast or source answers.
	ObjectsOnly bool `json:"-"`
}

// compact is the JSON the LLM sees: no traces or full model, just facts.
func (s Snapshot) compact() map[string]any {
	type rec struct {
		Rank        int      `json:"rank"`
		Action      string   `json:"action"`
		Name        string   `json:"name"`
		Risk        string   `json:"risk"`
		Improvement float64  `json:"improvement"`
		Closes      []string `json:"closes"`
		Opens       []string `json:"opens"`
	}
	var recs []rec
	for _, r := range s.Plan {
		recs = append(recs, rec{r.Rank, r.Action, r.Name, string(r.Risk), r.Improvement, r.Result.GapsClosed, r.Result.GapsOpened})
	}
	var kpis []map[string]any
	if s.Model != nil {
		for _, k := range s.Model.KPIs {
			e := map[string]any{"id": k.ID, "name": k.Name, "value": k.Value, "unit": k.Unit}
			if k.Target != nil {
				e["target"] = *k.Target
				e["direction"] = k.Direction
			}
			kpis = append(kpis, e)
		}
	}
	out := map[string]any{
		"kpis": kpis, "gaps": s.Gaps, "severity_total": s.Severity, "plan": recs,
		"anomalies": s.Anomalies, "forecasts": s.Forecasts, "sources": s.Sources,
	}
	if p := s.pack(); p != "" {
		out["pack"] = p
	}
	return out
}

// pack names the pack the snapshot comes from ("shop@0.1.0"), or "".
func (s Snapshot) pack() string {
	if s.Model == nil || s.Model.Pack == nil {
		return ""
	}
	if s.Model.Pack.Version != "" {
		return s.Model.Pack.ID + "@" + s.Model.Pack.Version
	}
	return s.Model.Pack.ID
}

type Engine struct {
	LLM *Provider
}

type Answer struct {
	Text      string   `json:"text"`
	Intent    string   `json:"intent"`
	Grounding []string `json:"grounding"`
	// Citations name the object properties an answer about business
	// objects rests on.
	Citations []Citation `json:"citations,omitempty"`
	Mode      string     `json:"mode"` // heuristic | llm
	Model     string     `json:"model,omitempty"`
	LLMError  string     `json:"llm_error,omitempty"`
}

type Status struct {
	Mode      string `json:"mode"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Mutations string `json:"mutations"`
}

func (e *Engine) Status() Status {
	s := Status{Mode: "heuristic", Mutations: "never — AI endpoints are read-only; actions run only after human approval"}
	if e.LLM != nil {
		s.Mode = "llm-rewrite"
		s.Provider = e.LLM.Label
		s.Model = e.LLM.Model
	}
	return s
}

func (e *Engine) finish(ctx context.Context, q string, a Answer, s Snapshot) Answer {
	a.Mode = "heuristic"
	if p := s.pack(); p != "" {
		a.Grounding = append([]string{"pack:" + p}, a.Grounding...)
	}
	if e.LLM == nil {
		return a
	}
	text, err := e.LLM.Rewrite(ctx, q, a.Text, s.compact())
	if err != nil {
		a.LLMError = err.Error()
		return a
	}
	a.Text, a.Mode, a.Model = text, "llm", e.LLM.Model
	return a
}

// Ask answers a free-text question from the snapshot.
func (e *Engine) Ask(ctx context.Context, q string, s Snapshot) Answer {
	if s.Objects != nil && e.LLM != nil && s.Objects.Select == nil {
		oc := *s.Objects
		oc.Select = e.LLM.selector(ctx)
		s.Objects = &oc
	}
	return e.finish(ctx, q, answer(q, s), s)
}

func answer(q string, s Snapshot) Answer {
	ql := strings.ToLower(q)
	if s.Objects != nil {
		if a, ok := answerObjectsQ(q, ql, s); ok {
			return a
		}
	}
	if s.ObjectsOnly {
		return Answer{Intent: "scope", Text: "I can answer questions about the business objects in your workspace: what they are, how they link, and which are exposed to a failing KPI."}
	}
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(ql, w) {
				return true
			}
		}
		return false
	}
	if s.Model != nil {
		if a, ok := matchAction(ql, s.Model); ok && has("what if", "simulate", "if we", "impact", "happen") {
			return simulateAnswer(a, s)
		}
		if k, ok := matchKPI(ql, s.Model); ok && has("why", "cause", "driv", "explain", "depend") {
			return whyAnswer(k, s)
		}
	}
	switch {
	case has("anomal", "unusual", "spike", "strange", "weird"):
		return anomalyAnswer(s)
	case has("forecast", "trend", "when will", "predict", "going to", "breach soon", "at risk"):
		return forecastAnswer(s)
	case has("should", "recommend", "fix", "what do", "next step", "best action", "plan"):
		return planAnswer(s)
	case has("source", "connect", "netra", "gravia", "fabric", "data", "health of"):
		return sourcesAnswer(s)
	case has("gap", "wrong", "miss", "breach", "problem", "issue", "red", "failing"):
		return gapsAnswer(s)
	}
	a := Digest(s)
	a.Intent = "summary"
	return a
}

func matchAction(q string, m *graph.Model) (graph.Action, bool) {
	for _, a := range m.Actions {
		if strings.Contains(q, strings.ToLower(a.ID)) || (a.Name != "" && strings.Contains(q, strings.ToLower(a.Name))) {
			return a, true
		}
	}
	return graph.Action{}, false
}

func matchKPI(q string, m *graph.Model) (graph.KPI, bool) {
	best, bestLen := graph.KPI{}, 0
	for _, k := range m.KPIs {
		for _, cand := range []string{strings.ToLower(k.ID), strings.ToLower(k.Name), strings.ReplaceAll(strings.ToLower(k.ID), "_", " ")} {
			if cand != "" && strings.Contains(q, cand) && len(cand) > bestLen {
				best, bestLen = k, len(cand)
			}
		}
	}
	return best, bestLen > 0
}

func gapsAnswer(s Snapshot) Answer {
	a := Answer{Intent: "gaps"}
	if len(s.Gaps) == 0 {
		a.Text = "All KPIs with targets are currently met."
		return a
	}
	var parts []string
	for i, g := range s.Gaps {
		if i == 4 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s at %s%s against a target of %s (%.0f%% off)", g.Name, fmtFloat(g.Value), unitSuffix(g.Unit), fmtFloat(g.Target), g.Severity*100))
		a.Grounding = append(a.Grounding, "gap:"+g.KPI)
	}
	a.Text = fmt.Sprintf("%d KPI(s) are missing target. Worst first: %s. Total gap severity is %s.", len(s.Gaps), strings.Join(parts, "; "), fmtFloat(s.Severity))
	return a
}

func planAnswer(s Snapshot) Answer {
	a := Answer{Intent: "plan"}
	if len(s.Plan) == 0 {
		if len(s.Gaps) == 0 {
			a.Text = "Nothing needs doing: every KPI is on target."
		} else {
			a.Text = "No modelled action reduces the total gap. Consider adding actions to the model for: " + gapNames(s.Gaps) + "."
		}
		return a
	}
	top := s.Plan[0]
	a.Grounding = append(a.Grounding, "plan:"+top.Action)
	a.Text = fmt.Sprintf("Best next step: %s (risk %s). It cuts total gap severity by %s", top.Name, orLow(top.Risk), fmtFloat(top.Improvement))
	if len(top.Result.GapsClosed) > 0 {
		a.Text += " and closes " + joinAnd(top.Result.GapsClosed)
	}
	a.Text += "."
	if len(top.Result.GapsOpened) > 0 {
		a.Text += " Trade-off: it would newly miss " + joinAnd(top.Result.GapsOpened) + "."
	}
	if len(s.Plan) > 1 {
		a.Text += fmt.Sprintf(" Runner-up: %s (gain %s).", s.Plan[1].Name, fmtFloat(s.Plan[1].Improvement))
		a.Grounding = append(a.Grounding, "plan:"+s.Plan[1].Action)
	}
	a.Text += " It needs approval before anything runs."
	return a
}

func simulateAnswer(act graph.Action, s Snapshot) Answer {
	a := Answer{Intent: "simulate", Grounding: []string{"action:" + act.ID}}
	r, err := sim.Apply(s.Model, act)
	if err != nil {
		a.Text = "Could not simulate " + act.ID + ": " + err.Error()
		return a
	}
	a.Text = Explain(r).Text
	return a
}

func whyAnswer(k graph.KPI, s Snapshot) Answer {
	a := Answer{Intent: "why", Grounding: []string{"kpi:" + k.ID}}
	var drivers, effects []string
	for _, e := range s.Model.Edges {
		if e.To == k.ID {
			d := fmt.Sprintf("%s (weight %+.3g", e.From, e.Weight)
			if e.Why != "" {
				d += ": " + e.Why
			}
			drivers = append(drivers, d+")")
		}
		if e.From == k.ID {
			effects = append(effects, e.To)
		}
	}
	a.Text = fmt.Sprintf("%s is %s%s", k.Name, fmtFloat(k.Value), unitSuffix(k.Unit))
	if k.Target != nil {
		op := "at least"
		if k.Direction == graph.LowerIsBetter {
			op = "at most"
		}
		a.Text += fmt.Sprintf(" (target %s %s)", op, fmtFloat(*k.Target))
	}
	a.Text += "."
	if len(drivers) > 0 {
		a.Text += " In the model it is driven by " + joinAnd(drivers) + "."
	} else {
		a.Text += " The model has no upstream drivers for it; it moves only through direct action effects."
	}
	if len(effects) > 0 {
		a.Text += " Changes to it propagate to " + joinAnd(effects) + "."
	}
	for _, an := range s.Anomalies {
		if an.KPI == k.ID {
			a.Text += " " + an.Text
		}
	}
	for _, f := range s.Forecasts {
		if f.KPI == k.ID && f.Status != "stable" {
			a.Text += " " + f.Text
		}
	}
	return a
}

func anomalyAnswer(s Snapshot) Answer {
	a := Answer{Intent: "anomalies"}
	if len(s.Anomalies) == 0 {
		a.Text = "No anomalies: every KPI is within 3 standard deviations of its recent baseline (or there is not enough history yet)."
		return a
	}
	var t []string
	for i, an := range s.Anomalies {
		if i == 3 {
			break
		}
		t = append(t, an.Text)
		a.Grounding = append(a.Grounding, "anomaly:"+an.KPI)
	}
	a.Text = strings.Join(t, " ")
	return a
}

func forecastAnswer(s Snapshot) Answer {
	a := Answer{Intent: "forecast"}
	var t []string
	for _, f := range s.Forecasts {
		if f.Status == "stable" {
			continue
		}
		t = append(t, f.Text)
		a.Grounding = append(a.Grounding, "forecast:"+f.KPI)
		if len(t) == 3 {
			break
		}
	}
	if len(t) == 0 {
		a.Text = "No KPI is projected to miss its target within 7 days on current trends."
		return a
	}
	a.Text = strings.Join(t, " ")
	return a
}

func sourcesAnswer(s Snapshot) Answer {
	a := Answer{Intent: "sources"}
	if len(s.Sources) == 0 {
		a.Text = "No live sources are configured; KPI values come from the model file."
		return a
	}
	var ok, bad []string
	for _, src := range s.Sources {
		a.Grounding = append(a.Grounding, "source:"+src.Name)
		if src.OK {
			ok = append(ok, fmt.Sprintf("%s (%d ms)", src.Name, src.LatencyMS))
		} else {
			bad = append(bad, src.Name+": "+src.Error)
		}
	}
	if len(ok) > 0 {
		a.Text = "Healthy sources: " + joinAnd(ok) + "."
	}
	if len(bad) > 0 {
		a.Text += " Failing: " + strings.Join(bad, "; ") + ". KPIs from failing sources keep their last value."
	}
	a.Text = strings.TrimSpace(a.Text)
	return a
}

// Digest is a two-to-three sentence status line for the top bar.
func Digest(s Snapshot) Answer {
	a := Answer{Intent: "digest"}
	var parts []string
	if len(s.Gaps) == 0 {
		parts = append(parts, "All KPIs are on target.")
	} else {
		parts = append(parts, fmt.Sprintf("%d KPI(s) off target, worst %s (%.0f%% off).", len(s.Gaps), s.Gaps[0].Name, s.Gaps[0].Severity*100))
		a.Grounding = append(a.Grounding, "gap:"+s.Gaps[0].KPI)
	}
	if len(s.Plan) > 0 {
		parts = append(parts, fmt.Sprintf("Top action: %s, pending approval.", s.Plan[0].Name))
		a.Grounding = append(a.Grounding, "plan:"+s.Plan[0].Action)
	}
	if len(s.Anomalies) > 0 {
		parts = append(parts, fmt.Sprintf("%d anomaly(ies), led by %s.", len(s.Anomalies), s.Anomalies[0].Name))
	}
	for _, f := range s.Forecasts {
		if f.Status == "at-risk" {
			parts = append(parts, f.Text)
			break
		}
	}
	var down []string
	for _, src := range s.Sources {
		if !src.OK {
			down = append(down, src.Name)
		}
	}
	if len(down) > 0 {
		sort.Strings(down)
		parts = append(parts, "Source(s) down: "+joinAnd(down)+".")
	}
	a.Text = strings.Join(parts, " ")
	a.Mode = "heuristic"
	return a
}

// Explain narrates a simulation result.
func Explain(r sim.Result) Answer {
	a := Answer{Intent: "explain", Grounding: []string{"action:" + r.Action}}
	var direct, chain []string
	for _, st := range r.Trace {
		if st.From == "" {
			direct = append(direct, fmt.Sprintf("%s %s", st.To, fmtPct(st.Delta)))
		} else {
			chain = append(chain, fmt.Sprintf("%s moves %s by %s", st.From, st.To, fmtPct(st.Delta)))
		}
	}
	a.Text = fmt.Sprintf("%s directly changes %s.", orID(r.ActionName, r.Action), joinAnd(direct))
	if len(chain) > 0 {
		a.Text += " Through the dependency graph, " + joinAnd(chain) + "."
	}
	a.Text += fmt.Sprintf(" Total gap severity goes from %s to %s.", fmtFloat(r.SeverityBefore), fmtFloat(r.SeverityAfter))
	if len(r.GapsClosed) > 0 {
		a.Text += " It closes " + joinAnd(r.GapsClosed) + "."
	}
	if len(r.GapsOpened) > 0 {
		a.Text += " It newly misses " + joinAnd(r.GapsOpened) + "."
	}
	return a
}

// ExplainWithLLM is Explain plus the optional rewrite.
func (e *Engine) Explain(ctx context.Context, r sim.Result, s Snapshot) Answer {
	return e.finish(ctx, "Explain the simulated impact of "+orID(r.ActionName, r.Action), Explain(r), s)
}

// Digest is Digest plus the optional rewrite.
func (e *Engine) Digest(ctx context.Context, s Snapshot) Answer {
	return e.finish(ctx, "Summarise the current status in two sentences", Digest(s), s)
}

func gapNames(g []gaps.Gap) string {
	var n []string
	for _, x := range g {
		n = append(n, x.Name)
	}
	return joinAnd(n)
}

func orLow(r graph.Risk) string {
	if r == "" {
		return "low"
	}
	return string(r)
}

func orID(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

const columnPrompt = `You map a placeholder in an action payload to one column of the source rows behind the action.
Answer with JSON {"column": "<one of COLUMNS>"} or {"column": ""} if none fits. Never invent a column or a value.`

// ChooseColumn asks the model which column holds the values for a fill
// placeholder. The caller checks the answer is one of cols and reads the
// values from the rows itself.
func (e *Engine) ChooseColumn(ctx context.Context, a graph.Action, name string, cols []string) (string, error) {
	if e.LLM == nil {
		return "", fmt.Errorf("no model configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	text, err := e.LLM.Chat(ctx, columnPrompt, fmt.Sprintf("ACTION: %s (%s)\nPLACEHOLDER: %s\nCOLUMNS: %s", a.ID, a.Name, name, strings.Join(cols, ", ")), true)
	if err != nil {
		return "", err
	}
	var out struct {
		Column string `json:"column"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return "", err
	}
	return out.Column, nil
}
