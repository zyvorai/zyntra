// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"bytes"
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// FileName is the optional ontology file inside a pack directory.
const FileName = "ontology.yaml"

// Definition is ontology.yaml: the schema, how source files become objects,
// the typed actions allowed on them, workflow views and the rollout shape.
type Definition struct {
	Name     string       `yaml:"name,omitempty" json:"name,omitempty"`
	Objects  []ObjectType `yaml:"objects" json:"objects"`
	Links    []LinkType   `yaml:"links" json:"links"`
	Mappings []Mapping    `yaml:"mappings,omitempty" json:"mappings,omitempty"`
	Actions  []ActionType `yaml:"actions,omitempty" json:"actions,omitempty"`
	Views    []ViewSpec   `yaml:"views,omitempty" json:"views,omitempty"`
	Rollout  *Rollout     `yaml:"rollout,omitempty" json:"rollout,omitempty"`
}

// Schema returns the type system part of the definition.
func (d *Definition) Schema() *Schema { return &Schema{Objects: d.Objects, Links: d.Links} }

// Mapping turns the rows of one source file into objects and links.
type Mapping struct {
	Source    string            `yaml:"source" json:"source"` // path relative to the pack
	Format    string            `yaml:"format,omitempty" json:"format,omitempty"`
	Type      string            `yaml:"type" json:"type"`
	Namespace string            `yaml:"namespace" json:"namespace"`
	Key       string            `yaml:"key" json:"key"`     // column holding the key
	Props     map[string]string `yaml:"props" json:"props"` // property -> column
	Aliases   []AliasMap        `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Links     []LinkMap         `yaml:"links,omitempty" json:"links,omitempty"`
	Tenant    string            `yaml:"tenant,omitempty" json:"tenant,omitempty"`
	// Observed names a column holding when the row was observed; empty
	// means the source file's read time.
	Observed string `yaml:"observed,omitempty" json:"observed,omitempty"`
}

// AliasMap names the identifier a column holds in another system.
type AliasMap struct {
	System string `yaml:"system" json:"system"`
	Column string `yaml:"column" json:"column"`
}

// LinkMap makes a link from each row's object to the object whose key is in
// Column.
type LinkMap struct {
	Type      string `yaml:"type" json:"type"`
	Column    string `yaml:"column" json:"column"`
	To        string `yaml:"to" json:"to"` // target object type
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
}

// ActionType is the typed contract around a model action.
type ActionType struct {
	ID          string        `yaml:"id" json:"id"` // a model action id
	Inputs      []Input       `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Affects     []string      `yaml:"affects,omitempty" json:"affects,omitempty"`
	Permissions []string      `yaml:"permissions,omitempty" json:"permissions,omitempty"` // roles; empty = any proposer
	Requires    []Requirement `yaml:"requires,omitempty" json:"requires,omitempty"`
	Outcome     []ObjectCheck `yaml:"outcome,omitempty" json:"outcome,omitempty"`
}

// Input is one typed argument of an action.
type Input struct {
	Name       string `yaml:"name" json:"name"`
	ObjectType string `yaml:"object_type" json:"object_type"`
	Required   bool   `yaml:"required,omitempty" json:"required,omitempty"`
}

// Requirement is an object-state precondition: the named input's property
// must (not) equal Value.
type Requirement struct {
	Input    string `yaml:"input" json:"input"`
	Property string `yaml:"property" json:"property"`
	Equals   string `yaml:"equals,omitempty" json:"equals,omitempty"`
	NotEq    string `yaml:"not_equals,omitempty" json:"not_equals,omitempty"`
}

// ObjectCheck is an outcome check: after the change, every object of the
// affected types must have no bound KPI missing its target.
type ObjectCheck struct {
	ObjectType string `yaml:"object_type" json:"object_type"`
	MustBeSafe bool   `yaml:"must_be_safe" json:"must_be_safe"`
}

// ViewSpec is a declarative workflow view over one object type.
type ViewSpec struct {
	ID      string   `yaml:"id" json:"id"`
	Title   string   `yaml:"title" json:"title"`
	Type    string   `yaml:"type" json:"type"`
	Columns []string `yaml:"columns" json:"columns"`
	// Where keeps objects whose property equals the value.
	Where   map[string]string `yaml:"where,omitempty" json:"where,omitempty"`
	Actions []string          `yaml:"actions,omitempty" json:"actions,omitempty"`
	// Exposed adds a column listing the at-risk objects this one depends on.
	Exposed bool `yaml:"exposed,omitempty" json:"exposed,omitempty"`
}

// Rollout describes how an approved decision reaches sites. Zyntra only
// states it; delivery belongs to the deployment tooling.
type Rollout struct {
	Stages []Stage `yaml:"stages" json:"stages"`
}

// Stage is one wave of a rollout with the health gates that must hold
// before the next wave starts.
type Stage struct {
	Name  string   `yaml:"name" json:"name"`
	Sites []string `yaml:"sites" json:"sites"`
	Gates []Gate   `yaml:"gates,omitempty" json:"gates,omitempty"`
}

// Gate is a KPI that must stay within a bound during a stage.
type Gate struct {
	KPI string   `yaml:"kpi" json:"kpi"`
	Max *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Min *float64 `yaml:"min,omitempty" json:"min,omitempty"`
}

// ParseDefinition decodes and validates ontology.yaml bytes.
func ParseDefinition(b []byte) (*Definition, error) {
	var d Definition
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// LoadDefinition reads ontology.yaml from a pack directory; it returns nil,
// nil when the pack has none.
func LoadDefinition(dir string) (*Definition, error) {
	path := dir + "/" + FileName
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := ParseDefinition(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return d, nil
}

// Validate checks the schema and everything that refers to it.
func (d *Definition) Validate() error {
	sc := d.Schema()
	errs := []error{sc.Validate()}
	for i, m := range d.Mappings {
		ot, ok := sc.Object(m.Type)
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("mapping %d: unknown object type %q", i, m.Type))
			continue
		case m.Source == "" || m.Key == "" || m.Namespace == "":
			errs = append(errs, fmt.Errorf("mapping %d (%s): source, namespace and key are required", i, m.Type))
		}
		for p := range m.Props {
			found := false
			for _, dp := range ot.Properties {
				found = found || dp.Name == p
			}
			if !found {
				errs = append(errs, fmt.Errorf("mapping %d (%s): unknown property %q", i, m.Type, p))
			}
		}
		for _, l := range m.Links {
			lt, ok := sc.Link(l.Type)
			if !ok || lt.From != m.Type || lt.To != l.To {
				errs = append(errs, fmt.Errorf("mapping %d (%s): link %q must join %s to %s", i, m.Type, l.Type, m.Type, l.To))
			}
		}
	}
	seen := map[string]bool{}
	for _, a := range d.Actions {
		if a.ID == "" || seen[a.ID] {
			errs = append(errs, fmt.Errorf("action %q: id missing or duplicated", a.ID))
		}
		seen[a.ID] = true
		names := map[string]bool{}
		for _, in := range a.Inputs {
			if _, ok := sc.Object(in.ObjectType); !ok || in.Name == "" {
				errs = append(errs, fmt.Errorf("action %s: input %q has unknown object type %q", a.ID, in.Name, in.ObjectType))
			}
			names[in.Name] = true
		}
		for _, r := range a.Requires {
			if !names[r.Input] {
				errs = append(errs, fmt.Errorf("action %s: requirement names unknown input %q", a.ID, r.Input))
			}
		}
		for _, t := range a.Affects {
			if _, ok := sc.Object(t); !ok {
				errs = append(errs, fmt.Errorf("action %s: affects unknown type %q", a.ID, t))
			}
		}
		for _, c := range a.Outcome {
			if _, ok := sc.Object(c.ObjectType); !ok {
				errs = append(errs, fmt.Errorf("action %s: outcome check on unknown type %q", a.ID, c.ObjectType))
			}
		}
	}
	vseen := map[string]bool{}
	for _, v := range d.Views {
		if _, ok := sc.Object(v.Type); !ok || v.ID == "" || vseen[v.ID] {
			errs = append(errs, fmt.Errorf("view %q: unknown type %q or duplicate id", v.ID, v.Type))
		}
		vseen[v.ID] = true
	}
	if d.Rollout != nil {
		for _, s := range d.Rollout.Stages {
			if s.Name == "" || len(s.Sites) == 0 {
				errs = append(errs, fmt.Errorf("rollout stage %q needs a name and sites", s.Name))
			}
		}
	}
	return errors.Join(errs...)
}

// CheckModel verifies every reference into the KPI model: bindings, outcome
// gates and action ids.
func (d *Definition) CheckModel(hasKPI, hasAction func(string) bool) error {
	errs := []error{d.Schema().CheckKPIs(hasKPI)}
	for _, a := range d.Actions {
		if !hasAction(a.ID) {
			errs = append(errs, fmt.Errorf("action %q is not an action of the KPI model", a.ID))
		}
	}
	if d.Rollout != nil {
		for _, s := range d.Rollout.Stages {
			for _, g := range s.Gates {
				if !hasKPI(g.KPI) {
					errs = append(errs, fmt.Errorf("rollout stage %s: gate on unknown KPI %q", s.Name, g.KPI))
				}
			}
		}
	}
	return errors.Join(errs...)
}
