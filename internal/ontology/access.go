// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"errors"
	"slices"
)

// Principal is who is asking. Retrieval and execution both take one.
type Principal struct {
	Subject string
	Roles   []string
	// Tenant confines the principal to one tenant's objects. Objects with no
	// tenant are hidden from it unless a rule lists their type in
	// SharedTypes.
	Tenant string
}

func (p Principal) has(role string) bool { return slices.Contains(p.Roles, role) }

// Rule is one access rule in the policy file. A rule applies to a principal
// holding any of its roles (every principal when Roles is empty).
type Rule struct {
	Roles []string `yaml:"roles,omitempty" json:"roles,omitempty"`
	// Tenant limits matching principals to objects of this tenant (plus
	// objects with no tenant).
	Tenant string `yaml:"tenant,omitempty" json:"tenant,omitempty"`
	// Types limits which object types are visible; the union across the
	// principal's rules applies. Empty means all types.
	Types []string `yaml:"object_types,omitempty" json:"object_types,omitempty"`
	// Deny hides properties; Allow reveals sensitive ones.
	Deny  []string `yaml:"deny_properties,omitempty" json:"deny_properties,omitempty"`
	Allow []string `yaml:"allow_properties,omitempty" json:"allow_properties,omitempty"`
	// Actions limits which typed actions may be proposed or run; the union
	// across the principal's rules applies. Empty means no extra limit.
	Actions []string `yaml:"actions,omitempty" json:"actions,omitempty"`
	// IngestTenants lists the tenants a matching principal may push records
	// into; "*" means any. Without a grant only admins may ingest.
	IngestTenants []string `yaml:"ingest_tenants,omitempty" json:"ingest_tenants,omitempty"`
	// SharedTypes lists object types with no tenant (provider-owned
	// infrastructure, say) that tenant-bound principals may read.
	SharedTypes []string `yaml:"shared_types,omitempty" json:"shared_types,omitempty"`
}

// Access enforces rules. With no rules the default holds: everyone sees every
// object, sensitive properties are visible to admins only.
type Access struct {
	Rules  []Rule
	Schema *Schema
}

func (a *Access) matching(p Principal) []Rule {
	var out []Rule
	for _, r := range a.Rules {
		if len(r.Roles) == 0 {
			out = append(out, r)
			continue
		}
		for _, role := range r.Roles {
			if p.has(role) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// Filter returns the object as p may see it, or false when p may not see it.
// The returned object is a copy; the store's data is never changed.
func (a *Access) Filter(p Principal, o Object) (Object, bool) {
	// A tenant-bound principal sees its own tenant's objects only, whether or
	// not any access rules are configured.
	if p.Tenant != "" && o.Tenant != p.Tenant {
		shared := false
		if o.Tenant == "" && a != nil {
			for _, r := range a.matching(p) {
				shared = shared || slices.Contains(r.SharedTypes, o.Type)
			}
		}
		if !shared {
			return Object{}, false
		}
	}
	if a == nil {
		return o, true
	}
	rules := a.matching(p)
	typed, typeOK := false, false
	allow := map[string]bool{}
	deny := map[string]bool{}
	for _, r := range rules {
		if r.Tenant != "" && o.Tenant != "" && o.Tenant != r.Tenant {
			return Object{}, false
		}
		if len(r.Types) > 0 {
			typed = true
			typeOK = typeOK || slices.Contains(r.Types, o.Type)
		}
		for _, n := range r.Allow {
			allow[n] = true
		}
		for _, n := range r.Deny {
			deny[n] = true
		}
	}
	if typed && !typeOK {
		return Object{}, false
	}
	sensitive := map[string]bool{}
	if a.Schema != nil {
		if ot, ok := a.Schema.Object(o.Type); ok {
			for _, pr := range ot.Properties {
				sensitive[pr.Name] = pr.Sensitive
			}
		}
	}
	out := o
	out.Props = make(map[string]Value, len(o.Props))
	for k, v := range o.Props {
		if deny[k] || (sensitive[k] && !allow[k] && !p.has("admin")) {
			continue
		}
		out.Props[k] = v
	}
	return out, true
}

// CanAct reports whether the access rules let p use the typed action.
func (a *Access) CanAct(p Principal, actionID string) bool {
	if a == nil {
		return true
	}
	limited, ok := false, false
	for _, r := range a.matching(p) {
		if len(r.Actions) > 0 {
			limited = true
			ok = ok || slices.Contains(r.Actions, actionID)
		}
	}
	return !limited || ok
}

// CanIngest reports whether p may push records into tenant ("" is the
// default tenant). Admins always may; anyone else needs an explicit grant.
func (a *Access) CanIngest(p Principal, tenant string) bool {
	if p.has("admin") {
		return true
	}
	if a == nil {
		return false
	}
	for _, r := range a.matching(p) {
		for _, t := range r.IngestTenants {
			if t == "*" || t == tenant {
				return true
			}
		}
	}
	return false
}

// Reader is the permission-checked view of a store for one principal. Every
// read path an assistant or API handler uses goes through it.
type Reader struct {
	st *Store
	ac *Access
	p  Principal
}

// As returns a reader scoped to p.
func (s *Store) As(a *Access, p Principal) Reader { return Reader{st: s, ac: a, p: p} }

// Principal returns who the reader acts for.
func (r Reader) Principal() Principal { return r.p }

func (r Reader) Get(id string) (Object, bool) {
	o, ok := r.st.Get(id)
	if !ok {
		return Object{}, false
	}
	return r.ac.Filter(r.p, o)
}

func (r Reader) List(typ string) []Object {
	var out []Object
	for _, o := range r.st.List(typ) {
		if f, ok := r.ac.Filter(r.p, o); ok {
			out = append(out, f)
		}
	}
	return out
}

// Links returns links touching id whose other end the principal can see.
func (r Reader) Links(id string) []Link {
	if _, ok := r.Get(id); !ok {
		return nil
	}
	var out []Link
	for _, l := range r.st.Links(id) {
		_, f := r.Get(l.From)
		_, t := r.Get(l.To)
		if f && t {
			out = append(out, l)
		}
	}
	return out
}

func (r Reader) Impact(id string, maxDepth int) []Impact {
	return r.st.impact(id, maxDepth, func(o Object) (Object, bool) { return r.ac.Filter(r.p, o) })
}

// Candidates are visible to approvers and admins only, and only when both
// objects of the pair are visible to them.
func (r Reader) Candidates() []Candidate {
	if !r.p.has("approver") && !r.p.has("admin") {
		return nil
	}
	var out []Candidate
	for _, c := range r.st.Candidates() {
		_, a := r.Get(c.A)
		_, b := r.Get(c.B)
		if (a && b) || c.Status != "pending" && r.p.Tenant == "" {
			out = append(out, c)
		}
	}
	return out
}

// Decide accepts or rejects a candidate the principal may see.
func (r Reader) Decide(id string, accept bool) (Candidate, error) {
	for _, c := range r.Candidates() {
		if c.ID == id {
			return r.st.Decide(id, accept, r.p.Subject)
		}
	}
	return Candidate{}, errors.New("no such candidate")
}

// Schema returns the store's schema.
func (s *Store) Schema() *Schema { return s.schema }

// History returns the changes to id that the principal may see. Visibility
// is judged now, not when the value was written: a property that has since
// become sensitive, denied or undeclared does not appear in old entries.
func (r Reader) History(id string) []Change {
	cur, ok := r.Get(id)
	if !ok {
		return nil
	}
	declared := map[string]bool{}
	if ot, ok := r.st.schema.Object(cur.Type); ok {
		for _, p := range ot.Properties {
			declared[p.Name] = true
		}
	}
	var out []Change
	for _, c := range r.st.History(cur.ID) {
		if _, visible := cur.Props[c.Property]; visible && declared[c.Property] {
			out = append(out, c)
		}
	}
	return out
}
