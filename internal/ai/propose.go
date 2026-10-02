// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"strings"

	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/ontology"
)

// Draft is a structured proposal request read out of free text. It is only a
// draft: nothing is created until a person with the propose role submits it,
// and ranking stays with the planner. The assistant never picks an action; it
// recognises one the user named.
type Draft struct {
	Action   string            `json:"action,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Valid    bool              `json:"valid"`
	Problems []string          `json:"problems,omitempty"`
}

// Structure finds the action and the objects a request names. types are the
// typed actions; validate is the registry's check for the caller.
func Structure(text string, m *graph.Model, types []ontology.ActionType, rd ontology.Reader,
	validate func(id string, inputs map[string]string) []string) Draft {
	ql := strings.ToLower(text)
	a, ok := matchAction(ql, m)
	if !ok {
		return Draft{Problems: []string{"name an action (by id or title) to draft a proposal"}}
	}
	d := Draft{Action: a.ID, Inputs: map[string]string{}}
	for _, t := range types {
		if t.ID != a.ID {
			continue
		}
		used := map[string]bool{}
		for _, in := range t.Inputs {
			for _, o := range rd.List(in.ObjectType) {
				n := strings.ToLower(name(o))
				if used[o.ID] || !(strings.Contains(ql, strings.ToLower(o.ID)) || (n != "" && strings.Contains(ql, n))) {
					continue
				}
				d.Inputs[in.Name], used[o.ID] = o.ID, true
				break
			}
		}
	}
	if validate != nil {
		d.Problems = validate(d.Action, d.Inputs)
	}
	d.Valid = len(d.Problems) == 0
	return d
}
