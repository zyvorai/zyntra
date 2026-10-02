// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

// ObjectContext is what the assistant may know about business objects. The
// reader enforces the caller's permissions on every access, so an answer can
// only cite what the asker could open themselves.
type ObjectContext struct {
	Reader  ontology.Reader
	Schema  *ontology.Schema
	Failing func(kpi string) bool
	// Select, when set, narrows candidate objects to the ones relevant to the
	// question. It may only choose among the candidates it is given and
	// returns their ids; an empty or invalid result is ignored and every
	// candidate is kept. Facts and citations are always rendered from the
	// records, never from the selector's output.
	Select func(question string, candidates []ontology.Object) []string
}

// narrow applies Select to candidates, keeping all of them when it fails.
func (oc *ObjectContext) narrow(question string, cands []ontology.Object) []ontology.Object {
	if oc.Select == nil || len(cands) < 2 {
		return cands
	}
	allowed := map[string]bool{}
	for _, o := range cands {
		allowed[o.ID] = true
	}
	keep := map[string]bool{}
	for _, id := range oc.Select(question, cands) {
		if allowed[id] {
			keep[id] = true
		}
	}
	if len(keep) == 0 {
		return cands
	}
	var out []ontology.Object
	for _, o := range cands {
		if keep[o.ID] {
			out = append(out, o)
		}
	}
	return out
}

// Citation points at one fact on one object.
type Citation struct {
	Object     string    `json:"object"`
	Property   string    `json:"property"`
	Value      any       `json:"value"`
	Source     string    `json:"source"`
	ObservedAt time.Time `json:"observed_at"`
}

func name(o ontology.Object) string {
	if v, ok := o.Props["name"]; ok {
		if s, ok := v.V.(string); ok && s != "" {
			return s
		}
	}
	return o.ID
}

func cite(o ontology.Object, prop string) (Citation, bool) {
	v, ok := o.Props[prop]
	if !ok {
		return Citation{}, false
	}
	return Citation{Object: o.ID, Property: prop, Value: v.V, Source: v.Prov.Source, ObservedAt: v.Prov.ObservedAt}, true
}

func (a *Answer) addCite(o ontology.Object, prop string) {
	if c, ok := cite(o, prop); ok {
		a.Citations = append(a.Citations, c)
		a.Grounding = append(a.Grounding, "object:"+o.ID+"."+prop)
	}
}

var riskWords = []string{"risk", "affect", "expose", "impact", "depend", "threat", "hurt", "fail", "breach"}

func hasAny(q string, words []string) bool {
	for _, w := range words {
		if strings.Contains(q, w) {
			return true
		}
	}
	return false
}

// answerObjects answers questions that name a business object or ask which
// objects are at risk. ql is the lower-cased question.
func answerObjects(ql string, s Snapshot) (Answer, bool) {
	return answerObjectsQ(ql, ql, s)
}

func answerObjectsQ(q, ql string, s Snapshot) (Answer, bool) {
	oc := s.Objects
	all := oc.Reader.List("")
	if len(all) == 0 {
		return Answer{}, false
	}
	var wantTypes []string
	for _, ot := range oc.Schema.Objects {
		n := strings.ToLower(ot.Name)
		if strings.Contains(ql, n) || strings.Contains(ql, n+"s") {
			wantTypes = append(wantTypes, ot.Name)
		}
	}
	var mentioned []ontology.Object
	for _, o := range all {
		n := strings.ToLower(name(o))
		if n != "" && strings.Contains(ql, n) || strings.Contains(ql, strings.ToLower(o.ID)) {
			mentioned = append(mentioned, o)
		}
	}
	risky := hasAny(ql, riskWords)
	switch {
	case risky && len(mentioned) == 0:
		return exposureAnswer(q, wantTypes, oc), true
	case len(mentioned) > 0:
		return describeAnswer(oc.narrow(q, mentioned)[0], risky, oc), true
	}
	return Answer{}, false
}

func exposureAnswer(q string, types []string, oc *ObjectContext) Answer {
	a := Answer{Intent: "objects"}
	risks := ontology.AtRisk(oc.Reader, oc.Failing)
	ex := ontology.Exposed(oc.Reader, risks)
	want := func(t string) bool {
		if len(types) == 0 {
			return true
		}
		for _, x := range types {
			if x == t {
				return true
			}
		}
		return false
	}
	type row struct {
		o    ontology.Object
		self []string
		deps []string
		kpis []string
	}
	rows := map[string]*row{}
	for _, r := range risks {
		if o, ok := oc.Reader.Get(r.ID); ok && want(o.Type) {
			rows[r.ID] = &row{o: o, self: r.KPIs, kpis: r.KPIs}
		}
	}
	for _, e := range ex {
		o, ok := oc.Reader.Get(e.ID)
		if !ok || !want(o.Type) {
			continue
		}
		r := rows[e.ID]
		if r == nil {
			r = &row{o: o}
			rows[e.ID] = r
		}
		r.deps, r.kpis = e.DependsOn, mergeStrings(r.kpis, e.KPIs)
	}
	if len(rows) == 0 {
		a.Text = "No visible object of that kind is failing a KPI or depends on something that is."
		return a
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if oc.Select != nil {
		cands := make([]ontology.Object, 0, len(ids))
		for _, id := range ids {
			cands = append(cands, rows[id].o)
		}
		ids = ids[:0]
		for _, o := range oc.narrow(q, cands) {
			ids = append(ids, o.ID)
		}
	}
	var lines []string
	for _, id := range ids {
		r := rows[id]
		a.addCite(r.o, "name")
		var why []string
		if len(r.self) > 0 {
			why = append(why, "fails "+strings.Join(r.self, ", "))
		}
		var depNames []string
		for _, d := range r.deps {
			if dep, ok := oc.Reader.Get(d); ok {
				depNames = append(depNames, name(dep))
				a.addCite(dep, "name")
			}
		}
		if len(depNames) > 0 {
			why = append(why, "depends on "+strings.Join(depNames, ", ")+" (failing "+strings.Join(r.kpis, ", ")+")")
		}
		lines = append(lines, fmt.Sprintf("- %s (%s): %s", name(r.o), r.o.Type, strings.Join(why, "; ")))
	}
	for _, r := range risks {
		for _, k := range r.KPIs {
			a.Grounding = append(a.Grounding, "kpi:"+k)
		}
	}
	a.Grounding = dedupe(a.Grounding)
	a.Text = fmt.Sprintf("%d object(s) are exposed. A link means connected, not how badly it will be hurt:\n%s", len(ids), strings.Join(lines, "\n"))
	return a
}

func describeAnswer(o ontology.Object, risky bool, oc *ObjectContext) Answer {
	a := Answer{Intent: "objects"}
	props := make([]string, 0, len(o.Props))
	for k := range o.Props {
		props = append(props, k)
	}
	sort.Strings(props)
	var parts []string
	for _, k := range props {
		parts = append(parts, fmt.Sprintf("%s=%v", k, o.Props[k].V))
		a.addCite(o, k)
	}
	text := fmt.Sprintf("%s is a %s (%s).", name(o), o.Type, strings.Join(parts, ", "))
	var links []string
	for _, l := range oc.Reader.Links(o.ID) {
		other, dir := l.To, "->"
		if l.To == o.ID {
			other, dir = l.From, "<-"
		}
		if x, ok := oc.Reader.Get(other); ok {
			links = append(links, fmt.Sprintf("%s %s %s", l.Type, dir, name(x)))
			a.addCite(x, "name")
		}
	}
	if len(links) > 0 {
		text += " Links: " + strings.Join(links, "; ") + "."
	}
	if bad := failingOf(o, oc); len(bad) > 0 {
		text += " It is failing: " + strings.Join(bad, ", ") + "."
		for _, k := range bad {
			a.Grounding = append(a.Grounding, "kpi:"+k)
		}
	}
	if risky {
		dep := oc.Reader.Impact(o.ID, 0)
		if len(dep) == 0 {
			text += " Nothing visible depends on it."
		} else {
			var n []string
			for _, d := range dep {
				n = append(n, name(d.Object))
				a.addCite(d.Object, "name")
			}
			text += " Depending on it: " + strings.Join(n, ", ") + "."
		}
	}
	a.Text = text
	return a
}

func failingOf(o ontology.Object, oc *ObjectContext) []string {
	var bad []string
	for _, k := range ontology.BoundKPIs(oc.Schema, o) {
		if oc.Failing(k) {
			bad = append(bad, k)
		}
	}
	return bad
}

func mergeStrings(a, b []string) []string {
	for _, x := range b {
		found := false
		for _, y := range a {
			found = found || x == y
		}
		if !found {
			a = append(a, x)
		}
	}
	sort.Strings(a)
	return a
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
