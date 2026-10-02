// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package ontology models business objects, the links between them and
// where each fact came from. It is deliberately separate from the KPI graph:
// links here say what depends on what, never by how much. The simulator does
// not import this package and this package does not import the simulator.
package ontology

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Property describes one field of an object type.
type Property struct {
	Name      string `json:"name" yaml:"name"`
	Type      string `json:"type" yaml:"type"` // string, number, bool, time
	Required  bool   `json:"required,omitempty" yaml:"required"`
	Sensitive bool   `json:"sensitive,omitempty" yaml:"sensitive"`
}

// ObjectType is a kind of business object such as Cluster or Service.
type ObjectType struct {
	Name       string     `json:"name" yaml:"name"`
	Properties []Property `json:"properties" yaml:"properties"`
	// KPIs lists the KPI ids that measure objects of this type. It is a
	// pointer into the KPI model, not a causal claim.
	KPIs []string `json:"kpis,omitempty" yaml:"kpis"`
	// Match names the property used to propose that two objects of this
	// type are the same thing (compared after normalising case, spacing
	// and punctuation). Matches are queued for review, never merged.
	Match string `json:"match,omitempty" yaml:"match"`
}

// LinkType is a named, directed relationship between two object types.
type LinkType struct {
	Name        string `json:"name" yaml:"name"`
	From        string `json:"from" yaml:"from"`
	To          string `json:"to" yaml:"to"`
	Cardinality string `json:"cardinality,omitempty" yaml:"cardinality"` // one-to-one, one-to-many, many-to-many
}

// Schema is the set of types a pack declares.
type Schema struct {
	Objects []ObjectType `json:"objects" yaml:"objects"`
	Links   []LinkType   `json:"links" yaml:"links"`
}

var propTypes = map[string]bool{"string": true, "number": true, "bool": true, "time": true}
var cardinalities = map[string]bool{"": true, "one-to-one": true, "one-to-many": true, "many-to-many": true}

// Object returns the named type.
func (s *Schema) Object(name string) (ObjectType, bool) {
	for _, o := range s.Objects {
		if o.Name == name {
			return o, true
		}
	}
	return ObjectType{}, false
}

// Link returns the named link type.
func (s *Schema) Link(name string) (LinkType, bool) {
	for _, l := range s.Links {
		if l.Name == name {
			return l, true
		}
	}
	return LinkType{}, false
}

// Validate reports every structural problem it finds, not just the first.
func (s *Schema) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for _, o := range s.Objects {
		switch {
		case o.Name == "":
			errs = append(errs, errors.New("object type with no name"))
			continue
		case strings.ContainsAny(o.Name, ": "):
			errs = append(errs, fmt.Errorf("object type %q: name may not contain ':' or spaces", o.Name))
		case seen[o.Name]:
			errs = append(errs, fmt.Errorf("duplicate object type %q", o.Name))
		}
		seen[o.Name] = true
		props := map[string]bool{}
		if o.Match != "" {
			found := false
			for _, p := range o.Properties {
				found = found || p.Name == o.Match
			}
			if !found {
				errs = append(errs, fmt.Errorf("object %q: match property %q is not declared", o.Name, o.Match))
			}
		}
		for _, p := range o.Properties {
			if p.Name == "" || !propTypes[p.Type] {
				errs = append(errs, fmt.Errorf("object %q: property %q has invalid type %q", o.Name, p.Name, p.Type))
			}
			if props[p.Name] {
				errs = append(errs, fmt.Errorf("object %q: duplicate property %q", o.Name, p.Name))
			}
			props[p.Name] = true
		}
	}
	links := map[string]bool{}
	for _, l := range s.Links {
		if l.Name == "" {
			errs = append(errs, errors.New("link type with no name"))
			continue
		}
		if links[l.Name] {
			errs = append(errs, fmt.Errorf("duplicate link type %q", l.Name))
		}
		links[l.Name] = true
		for _, end := range []string{l.From, l.To} {
			if !seen[end] {
				errs = append(errs, fmt.Errorf("link %q: unknown object type %q", l.Name, end))
			}
		}
		if !cardinalities[l.Cardinality] {
			errs = append(errs, fmt.Errorf("link %q: unknown cardinality %q", l.Name, l.Cardinality))
		}
	}
	return errors.Join(errs...)
}

// CheckKPIs verifies every KPI binding names a KPI the model has.
func (s *Schema) CheckKPIs(known func(id string) bool) error {
	var errs []error
	for _, o := range s.Objects {
		for _, k := range o.KPIs {
			if !known(k) {
				errs = append(errs, fmt.Errorf("object %q is bound to unknown KPI %q", o.Name, k))
			}
		}
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errors.Join(errs...)
}
