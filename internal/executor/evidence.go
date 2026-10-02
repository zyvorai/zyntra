// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package executor

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/graph"
)

// Evidence is the source rows a payload cites and the values filled from
// them. It is collected when a proposal is made and stored on it, so the
// payload that runs is the one that was approved.
type Evidence struct {
	// Rows holds the rows behind each KPI the payload reads.
	Rows map[string][]adapters.Row `json:"rows,omitempty"`
	// Fills are the values for fill:NAME placeholders.
	Fills map[string]Fill `json:"fills,omitempty"`
}

// Fill is one placeholder and where its values came from.
type Fill struct {
	Column string `json:"column"`
	Values []any  `json:"values"`
	// KPIs are the KPIs whose rows were read.
	KPIs []string `json:"kpis"`
	// By is "column" when the placeholder names a column, or "model" when a
	// model chose the column; the values always come from the rows.
	By string `json:"by"`
}

var (
	rowsRef = regexp.MustCompile(`\brows\s+"([^"]+)"`)
	fillRef = regexp.MustCompile(`\bfill\s+"([^"]+)"`)
)

// Needs lists the KPIs whose rows an action reads and the fill names it
// uses, from its webhook body, file templates and kubectl params.
func Needs(a graph.Action) (rows, fills []string) {
	seenR, seenF := map[string]bool{}, map[string]bool{}
	addR := func(id string) {
		if !seenR[id] {
			seenR[id] = true
			rows = append(rows, id)
		}
	}
	addF := func(n string) {
		if !seenF[n] {
			seenF[n] = true
			fills = append(fills, n)
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if id, ok := strings.CutPrefix(t, "rows:"); ok {
				addR(id)
			}
			if n, ok := strings.CutPrefix(t, "fill:"); ok {
				addF(n)
			}
		case map[string]any:
			for _, k := range sortedAnyKeys(t) {
				walk(t[k])
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	if a.Webhook != nil {
		walk(a.Webhook.Body)
	}
	if a.File != nil {
		for _, txt := range []string{a.File.Path, a.File.Content} {
			for _, g := range rowsRef.FindAllStringSubmatch(txt, -1) {
				addR(g[1])
			}
			for _, g := range fillRef.FindAllStringSubmatch(txt, -1) {
				addF(g[1])
			}
		}
	}
	if a.Execute != nil {
		for _, k := range sortedKeys(a.Execute.Params) {
			walk(a.Execute.Params[k])
		}
	}
	if len(fills) > 0 {
		for _, e := range a.Effects {
			addR(e.KPI)
		}
	}
	return rows, fills
}

// Collect reads the rows an action needs and fills placeholders whose name
// is a column of those rows. Placeholders it cannot fill are left out; the
// render then fails, so the proposal cannot be approved until they are.
func Collect(m *graph.Model, a graph.Action, files *adapters.FileCache) (*Evidence, error) {
	need, fills := Needs(a)
	if len(need) == 0 {
		return nil, nil
	}
	ev := &Evidence{Rows: map[string][]adapters.Row{}}
	for _, id := range need {
		k, ok := m.KPI(id)
		if !ok {
			return nil, fmt.Errorf("rows %q: no such KPI", id)
		}
		rows, err := adapters.Rows(m, *k, files)
		if err != nil {
			return nil, fmt.Errorf("rows %s: %w", id, err)
		}
		ev.Rows[id] = rows
	}
	for _, n := range fills {
		if f, ok := ev.FillFrom(a, n, n, "column"); ok {
			if ev.Fills == nil {
				ev.Fills = map[string]Fill{}
			}
			ev.Fills[n] = f
		}
	}
	return ev, nil
}

// FillFrom takes the distinct values of column from the rows behind the
// action's effect KPIs, in row order.
func (ev *Evidence) FillFrom(a graph.Action, name, column, by string) (Fill, bool) {
	f := Fill{Column: column, By: by}
	seen := map[string]bool{}
	for _, e := range a.Effects {
		rows := ev.Rows[e.KPI]
		used := false
		for _, r := range rows {
			v, ok := r.Values[column]
			if !ok {
				continue
			}
			used = true
			if key := fmt.Sprint(v); !seen[key] {
				seen[key] = true
				f.Values = append(f.Values, v)
			}
		}
		if used {
			f.KPIs = append(f.KPIs, e.KPI)
		}
	}
	return f, len(f.KPIs) > 0
}

// Columns lists the columns of the rows behind the action's effect KPIs.
func (ev *Evidence) Columns(a graph.Action) []string {
	set := map[string]bool{}
	for _, e := range a.Effects {
		for _, r := range ev.Rows[e.KPI] {
			for c := range r.Values {
				set[c] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (ev *Evidence) rows(id string) ([]map[string]any, error) {
	if ev == nil {
		return nil, fmt.Errorf("rows %q: no evidence collected", id)
	}
	rs, ok := ev.Rows[id]
	if !ok {
		return nil, fmt.Errorf("rows %q: not collected", id)
	}
	out := make([]map[string]any, len(rs))
	for i, r := range rs {
		out[i] = maps.Clone(r.Values)
	}
	return out, nil
}

func (ev *Evidence) fill(name string) (Fill, error) {
	if ev != nil {
		if f, ok := ev.Fills[name]; ok {
			return f, nil
		}
	}
	return Fill{}, fmt.Errorf("fill %q: no column of that name in the rows behind the action's KPIs, and no model chose one", name)
}

// cited summarises the rows a payload used, for the dry-run display.
func (ev *Evidence) cited() string {
	if ev == nil || len(ev.Rows) == 0 {
		return ""
	}
	var lines []string
	for _, id := range sortedRowKeys(ev.Rows) {
		rs := ev.Rows[id]
		if len(rs) == 0 {
			lines = append(lines, fmt.Sprintf("# rows used for %s: none matched", id))
			continue
		}
		lines = append(lines, fmt.Sprintf("# rows used for %s: %s rows %s", id, rs[0].Source, spans(rs)))
	}
	for _, n := range sortedFillKeys(ev.Fills) {
		f := ev.Fills[n]
		lines = append(lines, fmt.Sprintf("# fill %s: column %s (%s) from %s", n, f.Column, f.By, strings.Join(f.KPIs, ", ")))
	}
	return strings.Join(lines, "\n") + "\n"
}

// spans prints 1-based row numbers with consecutive runs collapsed: "1-40, 48".
func spans(rs []adapters.Row) string {
	var out []string
	for i := 0; i < len(rs); {
		j := i
		for j+1 < len(rs) && rs[j+1].Index == rs[j].Index+1 {
			j++
		}
		if j > i {
			out = append(out, fmt.Sprintf("%d-%d", rs[i].Index, rs[j].Index))
		} else {
			out = append(out, fmt.Sprint(rs[i].Index))
		}
		i = j + 1
	}
	return strings.Join(out, ", ")
}

func sortedAnyKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedRowKeys(m map[string][]adapters.Row) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedFillKeys(m map[string]Fill) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
