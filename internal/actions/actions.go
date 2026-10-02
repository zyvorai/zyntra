// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package actions is the typed action registry: the contract around a model
// action saying which objects it takes, who may propose it, what must be true
// of those objects first and how success is checked afterwards. The change
// itself still runs through the existing approval and executor path.
package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

// Registry holds the typed actions of one ontology.
type Registry struct {
	defs   map[string]ontology.ActionType
	store  *ontology.Store
	access *ontology.Access
	// Now supplies the clock for evidence-age checks; tests replace it.
	Now func() time.Time
}

// New builds a registry; a nil definition yields an empty registry.
func New(def *ontology.Definition, st *ontology.Store, ac *ontology.Access) *Registry {
	r := &Registry{defs: map[string]ontology.ActionType{}, store: st, access: ac, Now: time.Now}
	if def != nil {
		for _, a := range def.Actions {
			r.defs[a.ID] = a
		}
	}
	return r
}

// Get returns one typed action.
func (r *Registry) Get(id string) (ontology.ActionType, bool) {
	a, ok := r.defs[id]
	return a, ok
}

// List returns every typed action, sorted by id.
func (r *Registry) List() []ontology.ActionType {
	out := make([]ontology.ActionType, 0, len(r.defs))
	for _, a := range r.defs {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Validate checks a proposal request and returns the objects it resolves to,
// or every problem found. hasRole reports the caller's roles; objects the
// caller may not see count as missing, so existence is not leaked.
func (r *Registry) Validate(id string, inputs map[string]string, p ontology.Principal, hasRole func(string) bool) ([]ontology.ObjectRef, []string) {
	a, ok := r.defs[id]
	if !ok {
		return nil, []string{fmt.Sprintf("%q is not a typed action", id)}
	}
	var problems []string
	if !r.access.CanAct(p, id) {
		problems = append(problems, "your access rules do not allow this action")
	}
	if len(a.Permissions) > 0 {
		allowed := hasRole("admin")
		for _, role := range a.Permissions {
			allowed = allowed || hasRole(role)
		}
		if !allowed {
			problems = append(problems, fmt.Sprintf("requires one of the roles: %v", a.Permissions))
		}
	}
	declared := map[string]ontology.Input{}
	for _, in := range a.Inputs {
		declared[in.Name] = in
	}
	for name := range inputs {
		if _, ok := declared[name]; !ok {
			problems = append(problems, fmt.Sprintf("unknown input %q", name))
		}
	}
	rd := r.store.As(r.access, p)
	got := map[string]ontology.Object{}
	var refs []ontology.ObjectRef
	for _, in := range a.Inputs {
		v, ok := inputs[in.Name]
		if !ok || v == "" {
			if in.Required {
				problems = append(problems, fmt.Sprintf("input %q is required", in.Name))
			}
			continue
		}
		o, ok := rd.Get(v)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("input %q: no object %q", in.Name, v))
		case o.Type != in.ObjectType:
			problems = append(problems, fmt.Sprintf("input %q wants a %s, %q is a %s", in.Name, in.ObjectType, v, o.Type))
		default:
			got[in.Name] = o
			refs = append(refs, ontology.ObjectRef{Input: in.Name, ID: o.ID, Type: o.Type})
		}
	}
	problems = append(problems, r.checkObjects(a, got, r.Now())...)
	if len(problems) > 0 {
		return nil, problems
	}
	return refs, nil
}

// CheckOutcome evaluates the action's object-level outcome checks: for each
// MustBeSafe check, the objects of that type among the action's objects and
// everything depending on them must have no failing bound KPI. It returns
// the objects that still fail.
func (r *Registry) CheckOutcome(id string, refs []ontology.ObjectRef, failing func(kpi string) bool) []ontology.Risk {
	a, ok := r.defs[id]
	if !ok {
		return nil
	}
	rd := r.store.As(nil, ontology.Principal{})
	var bad []ontology.Risk
	seen := map[string]bool{}
	for _, c := range a.Outcome {
		if !c.MustBeSafe {
			continue
		}
		scope := map[string]bool{}
		for _, ref := range refs {
			scope[ref.ID] = true
			for _, im := range rd.Impact(ref.ID, 0) {
				scope[im.Object.ID] = true
			}
		}
		for _, rk := range ontology.AtRisk(rd, failing) {
			if rk.Type == c.ObjectType && scope[rk.ID] && !seen[rk.ID] {
				seen[rk.ID] = true
				bad = append(bad, rk)
			}
		}
	}
	slices.SortFunc(bad, func(a, b ontology.Risk) int { return compare(a.ID, b.ID) })
	return bad
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// checkObjects applies the object-state requirements and the evidence rules
// to the objects an action would act on.
func (r *Registry) checkObjects(a ontology.ActionType, got map[string]ontology.Object, now time.Time) []string {
	var problems []string
	for _, req := range a.Requires {
		o, ok := got[req.Input]
		if !ok {
			continue
		}
		have := ""
		if v, ok := o.Props[req.Property]; ok {
			have = fmt.Sprint(v.V)
		}
		if req.Equals != "" && have != req.Equals {
			problems = append(problems, fmt.Sprintf("%s.%s is %q, must be %q", o.ID, req.Property, have, req.Equals))
		}
		if req.NotEq != "" && have == req.NotEq {
			problems = append(problems, fmt.Sprintf("%s.%s must not be %q", o.ID, req.Property, req.NotEq))
		}
	}
	for _, e := range a.Evidence {
		o, ok := got[e.Input]
		if !ok {
			continue
		}
		props := e.Properties
		if len(props) == 0 {
			for k := range o.Props {
				props = append(props, k)
			}
			sort.Strings(props)
		}
		var maxAge time.Duration
		if e.MaxAge != "" {
			maxAge, _ = time.ParseDuration(e.MaxAge)
		}
		for _, name := range props {
			v, ok := o.Props[name]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("%s: required evidence %q is missing", o.ID, name))
			case maxAge > 0 && (v.Prov.ObservedAt.IsZero() || now.Sub(v.Prov.ObservedAt) > maxAge):
				problems = append(problems, fmt.Sprintf("%s.%s is stale: observed %s, newer than %s is required",
					o.ID, name, v.Prov.ObservedAt.UTC().Format(time.RFC3339), e.MaxAge))
			}
		}
	}
	return problems
}

// Digest fingerprints the action's contract and the objects it acts on, for
// Recheck to compare against later.
func (r *Registry) Digest(id string, refs []ontology.ObjectRef) string {
	b, _ := json.Marshal(r.defs[id])
	h := sha256.New()
	h.Write(b)
	h.Write([]byte(r.store.Digest(refs)))
	return hex.EncodeToString(h.Sum(nil))
}

// Recheck is Validate's second look, run right before execution against the
// stored proposal: the contract and the objects' facts must be what they were
// when the proposal was made, and the object-state and evidence rules must
// still hold now. It does not depend on who is asking; the approval already
// carries the authority.
func (r *Registry) Recheck(id string, refs []ontology.ObjectRef, digest string) []string {
	a, ok := r.defs[id]
	if !ok {
		return []string{fmt.Sprintf("%q is no longer a typed action", id)}
	}
	var problems []string
	if digest != "" && r.Digest(id, refs) != digest {
		problems = append(problems, "the objects or the action contract changed after this proposal was made; propose it again")
	}
	got := map[string]ontology.Object{}
	for _, ref := range refs {
		o, ok := r.store.Get(ref.ID)
		if !ok {
			problems = append(problems, fmt.Sprintf("object %q no longer exists", ref.ID))
			continue
		}
		got[ref.Input] = o
	}
	return append(problems, r.checkObjects(a, got, r.Now())...)
}
