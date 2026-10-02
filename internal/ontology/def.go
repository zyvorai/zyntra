// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"time"

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
	// Connectors are extra record sources beyond the file mappings.
	Connectors []ConnectorSpec `yaml:"connectors,omitempty" json:"connectors,omitempty"`
}

// ConnectorSpec configures one connector. Kind is exec (a program writing
// JSON-lines records, enabled only when ZYNTRA_CONNECTOR_EXEC=1) or http (a
// GET returning a JSON array of records).
type ConnectorSpec struct {
	Name    string   `yaml:"name" json:"name"`
	Kind    string   `yaml:"kind" json:"kind"`
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	URL     string   `yaml:"url,omitempty" json:"url,omitempty"`
	// TokenEnv names the environment variable holding a bearer token.
	TokenEnv string `yaml:"token_env,omitempty" json:"token_env,omitempty"`
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
	// Evidence says which facts about an input must be present and how old
	// they may be. It is checked when the action is proposed and again
	// right before it runs.
	Evidence []EvidenceRule `yaml:"evidence,omitempty" json:"evidence,omitempty"`
}

// EvidenceRule constrains the facts behind one input. Properties names the
// properties that must be present (all of the object's when empty); MaxAge,
// when set, is the oldest observation that still counts, e.g. "1h".
type EvidenceRule struct {
	Input      string   `yaml:"input" json:"input"`
	Properties []string `yaml:"properties,omitempty" json:"properties,omitempty"`
	MaxAge     string   `yaml:"max_age,omitempty" json:"max_age,omitempty"`
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
		for _, e := range a.Evidence {
			if !names[e.Input] {
				errs = append(errs, fmt.Errorf("action %s: evidence rule names unknown input %q", a.ID, e.Input))
			}
			if e.MaxAge != "" {
				if d, err := time.ParseDuration(e.MaxAge); err != nil || d <= 0 {
					errs = append(errs, fmt.Errorf("action %s: evidence max_age %q is not a positive duration", a.ID, e.MaxAge))
				}
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
	cseen := map[string]bool{}
	for _, c := range d.Connectors {
		switch {
		case c.Name == "" || cseen[c.Name]:
			errs = append(errs, fmt.Errorf("connector %q: name missing or duplicated", c.Name))
		case c.Kind == "exec" && len(c.Command) == 0, c.Kind == "http" && c.URL == "":
			errs = append(errs, fmt.Errorf("connector %s: %s needs a command or url", c.Name, c.Kind))
		case c.Kind != "exec" && c.Kind != "http":
			errs = append(errs, fmt.Errorf("connector %s: unknown kind %q", c.Name, c.Kind))
		}
		cseen[c.Name] = true
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

// ObjectRef names an object an action works on, as recorded on a proposal.
type ObjectRef struct {
	Input string `json:"input"`
	ID    string `json:"id"`
	Type  string `json:"type"`
}
