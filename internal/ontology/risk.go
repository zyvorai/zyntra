// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"sort"
	"strings"
)

// BoundKPIs lists the KPIs that measure an object: its own "kpis" property
// (comma separated) when set, otherwise the ones its type declares.
func BoundKPIs(schema *Schema, o Object) []string {
	if v, ok := o.Props["kpis"]; ok {
		var out []string
		for _, k := range strings.Split(strings.TrimSpace(toString(v.V)), ",") {
			if k = strings.TrimSpace(k); k != "" {
				out = append(out, k)
			}
		}
		return out
	}
	ot, _ := schema.Object(o.Type)
	return ot.KPIs
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// Risk is an object with at least one bound KPI missing its target.
type Risk struct {
	ID   string   `json:"id"`
	Type string   `json:"type"`
	KPIs []string `json:"kpis"` // the failing KPIs
}

// Exposure is an object that depends on at-risk objects.
type Exposure struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	DependsOn []string `json:"depends_on"` // at-risk object ids
	KPIs      []string `json:"kpis"`       // their failing KPIs, deduplicated
}

// AtRisk finds objects with a failing bound KPI. failing reports whether a
// KPI currently misses its target. This is a lookup, not a prediction.
func AtRisk(r Reader, failing func(kpi string) bool) []Risk {
	var out []Risk
	for _, o := range r.Bound() {
		var bad []string
		for _, k := range BoundKPIs(r.st.schema, o) {
			if failing(k) {
				bad = append(bad, k)
			}
		}
		if len(bad) > 0 {
			out = append(out, Risk{ID: o.ID, Type: o.Type, KPIs: bad})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Exposed lists objects that depend, through links, on at-risk objects. It
// says what is connected to a problem; it does not say how badly it will be
// hurt.
func Exposed(r Reader, risks []Risk) []Exposure {
	by := map[string]*Exposure{}
	for _, rk := range risks {
		for _, im := range r.Impact(rk.ID, 0) {
			e := by[im.Object.ID]
			if e == nil {
				e = &Exposure{ID: im.Object.ID, Type: im.Object.Type}
				by[e.ID] = e
			}
			e.DependsOn = append(e.DependsOn, rk.ID)
			for _, k := range rk.KPIs {
				if !contains(e.KPIs, k) {
					e.KPIs = append(e.KPIs, k)
				}
			}
		}
	}
	out := make([]Exposure, 0, len(by))
	for _, e := range by {
		sort.Strings(e.DependsOn)
		sort.Strings(e.KPIs)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
