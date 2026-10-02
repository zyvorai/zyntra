// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
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
	// RefreshInterval is how often the pack's file mappings are re-read
	// (default 1m).
	RefreshInterval string `yaml:"refresh_interval,omitempty" json:"refresh_interval,omitempty"`
}

// ConnectorSpec configures one connector. Kind is exec (a program writing
// JSON-lines records, enabled only when ZYNTRA_CONNECTOR_EXEC=1), http (a
// GET returning a JSON array of records), kubernetes, sql or rest (a paged
// JSON API mapped like kubernetes and sql).
type ConnectorSpec struct {
	Name    string   `yaml:"name" json:"name"`
	Kind    string   `yaml:"kind" json:"kind"`
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	URL     string   `yaml:"url,omitempty" json:"url,omitempty"`
	// TokenEnv names the environment variable holding a bearer token.
	TokenEnv string `yaml:"token_env,omitempty" json:"token_env,omitempty"`
	// Interval is how often the scheduler runs the connector (e.g. "5m");
	// empty means every 5 minutes.
	Interval string `yaml:"interval,omitempty" json:"interval,omitempty"`
	// Timeout bounds one run (default 1m).
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	// kubernetes: kubectl get Resource -o json, each item flattened to a row
	// by Fields (column -> dotted path, "\\." escapes a dot), then mapped by
	// Mapping. K8sNamespace "" reads all namespaces.
	Resource     string `yaml:"resource,omitempty" json:"resource,omitempty"`
	K8sNamespace string `yaml:"k8s_namespace,omitempty" json:"k8s_namespace,omitempty"`
	// K8sSelector and K8sFieldSelector narrow the list server-side (kubectl
	// -l / --field-selector). On a large cluster this is the difference
	// between a few kilobytes and a hundred megabytes per pull.
	K8sSelector      string            `yaml:"k8s_selector,omitempty" json:"k8s_selector,omitempty"`
	K8sFieldSelector string            `yaml:"k8s_field_selector,omitempty" json:"k8s_field_selector,omitempty"`
	Fields           map[string]string `yaml:"fields,omitempty" json:"fields,omitempty"`

	// sql: Query runs read-only with one argument, the time of the last
	// successful run (RFC 3339; 1970 on the first), so it can select changes
	// only. The connection string comes from the environment variable DSNEnv,
	// never from the pack. Driver is postgres or sqlite.
	Driver string `yaml:"driver,omitempty" json:"driver,omitempty"`
	DSNEnv string `yaml:"dsn_env,omitempty" json:"dsn_env,omitempty"`
	Query  string `yaml:"query,omitempty" json:"query,omitempty"`
	// ReconcileQuery finds rows deleted at the source when Query returns
	// changes only. It is a parameterless read-only listing of the rows that
	// still exist, with at least the columns that identify an object; every
	// ReconcileEvery (default 24h) the objects this connector alone created
	// and that it no longer lists are removed. An empty listing never prunes.
	ReconcileQuery string `yaml:"reconcile_query,omitempty" json:"reconcile_query,omitempty"`
	ReconcileEvery string `yaml:"reconcile_every,omitempty" json:"reconcile_every,omitempty"`

	// Prune removes objects this connector created that a later full listing
	// no longer contains, so a deleted pod or row disappears from the ontology
	// instead of lingering. Kubernetes, or a sql query with no parameter (a
	// query that filters on the last-run time returns changes, not everything).
	// Off by default.
	Prune bool `yaml:"prune,omitempty" json:"prune,omitempty"`

	// rest: GET URL, read the list at Items (a dotted path; empty means the
	// body is the list), flatten each item with Fields and map it. Paging is
	// either Next (a dotted path to the next page's URL; a dot inside a key is escaped, so
	// OData's is written @odata\.nextLink)
	// or PageParam (a query parameter counted from 1, with PageSize items).
	// SinceParam, when set, is a query parameter that receives the last
	// successful run's time. The bearer token is TokenEnv; for basic auth use
	// UserEnv and PassEnv. Follow-up URLs must stay on the host of URL.
	Items     string `yaml:"items,omitempty" json:"items,omitempty"`
	Next      string `yaml:"next,omitempty" json:"next,omitempty"`
	PageParam string `yaml:"page_param,omitempty" json:"page_param,omitempty"`
	PageSize  int    `yaml:"page_size,omitempty" json:"page_size,omitempty"`
	// PageSizeParam names the query parameter that carries PageSize (default
	// "limit"; OData uses "$top", many APIs "per_page").
	PageSizeParam string `yaml:"page_size_param,omitempty" json:"page_size_param,omitempty"`
	SinceParam    string `yaml:"since_param,omitempty" json:"since_param,omitempty"`
	UserEnv       string `yaml:"user_env,omitempty" json:"user_env,omitempty"`
	PassEnv       string `yaml:"pass_env,omitempty" json:"pass_env,omitempty"`

	// Mapping turns rows (kubernetes, sql, rest) into objects; its Source is unused.
	Mapping *Mapping `yaml:"mapping,omitempty" json:"mapping,omitempty"`
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
		errs = append(errs, validateMapping(sc, m, fmt.Sprintf("mapping %d", i), true)...)
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
		label := "connector " + c.Name
		if c.Name == "" || cseen[c.Name] || c.Name == PackFilesJob {
			errs = append(errs, fmt.Errorf("connector %q: name missing, duplicated or reserved", c.Name))
		}
		cseen[c.Name] = true
		for field, v := range map[string]string{"interval": c.Interval, "timeout": c.Timeout} {
			if v == "" {
				continue
			}
			dur, err := time.ParseDuration(v)
			if err != nil || dur < 10*time.Second && field == "interval" || dur <= 0 {
				errs = append(errs, fmt.Errorf("%s: %s %q must be a duration of at least 10s", label, field, v))
			}
		}
		switch c.Kind {
		case "exec":
			if len(c.Command) == 0 {
				errs = append(errs, fmt.Errorf("%s: exec needs a command", label))
			}
		case "http":
			if c.URL == "" {
				errs = append(errs, fmt.Errorf("%s: http needs a url", label))
			}
		case "kubernetes":
			if !kubeName.MatchString(c.Resource) || (c.K8sNamespace != "" && !kubeName.MatchString(c.K8sNamespace)) {
				errs = append(errs, fmt.Errorf("%s: resource and k8s_namespace must be plain kubernetes names", label))
			}
			if len(c.Fields) == 0 || c.Mapping == nil {
				errs = append(errs, fmt.Errorf("%s: kubernetes needs fields and a mapping", label))
			}
			if strings.EqualFold(c.Resource, "secrets") {
				errs = append(errs, fmt.Errorf("%s: a connector may not read secrets", label))
			}
			for _, sel := range []string{c.K8sSelector, c.K8sFieldSelector} {
				if !selectorText.MatchString(sel) {
					errs = append(errs, fmt.Errorf("%s: selector %q has characters a kubernetes selector never uses", label, sel))
				}
			}
		case "rest":
			if u, err := url.Parse(c.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: rest needs an http(s) url", label))
			}
			if len(c.Fields) == 0 || c.Mapping == nil {
				errs = append(errs, fmt.Errorf("%s: rest needs fields and a mapping", label))
			}
			if c.Next != "" && c.PageParam != "" {
				errs = append(errs, fmt.Errorf("%s: use next or page_param, not both", label))
			}
			for _, pn := range []string{c.PageParam, c.SinceParam, c.PageSizeParam} {
				if pn != "" && !paramName.MatchString(pn) {
					errs = append(errs, fmt.Errorf("%s: %q is not a plain query parameter name", label, pn))
				}
			}
			if c.PageSize < 0 || c.PageSize > 10000 {
				errs = append(errs, fmt.Errorf("%s: page_size must be 0-10000", label))
			}
			if (c.UserEnv == "") != (c.PassEnv == "") || (c.TokenEnv != "" && c.UserEnv != "") {
				errs = append(errs, fmt.Errorf("%s: give a token_env, or user_env with pass_env, not both", label))
			}
		case "sql":
			if c.Driver != "postgres" && c.Driver != "sqlite" {
				errs = append(errs, fmt.Errorf("%s: driver must be postgres or sqlite", label))
			}
			if c.DSNEnv == "" || c.Query == "" || c.Mapping == nil {
				errs = append(errs, fmt.Errorf("%s: sql needs dsn_env, query and a mapping", label))
			}
			if err := CheckReadOnlyQuery(c.Query); err != nil && c.Query != "" {
				errs = append(errs, fmt.Errorf("%s: %w", label, err))
			}
			if c.ReconcileQuery != "" {
				switch {
				case !QueryHasParam(c.Query):
					errs = append(errs, fmt.Errorf("%s: reconcile_query is for a query that returns changes only; this one is a full listing, use prune", label))
				case QueryHasParam(c.ReconcileQuery):
					errs = append(errs, fmt.Errorf("%s: reconcile_query must list every row, so it takes no parameter", label))
				}
				if err := CheckReadOnlyQuery(c.ReconcileQuery); err != nil {
					errs = append(errs, fmt.Errorf("%s: reconcile_query: %w", label, err))
				}
			}
		default:
			errs = append(errs, fmt.Errorf("%s: unknown kind %q", label, c.Kind))
		}
		if c.ReconcileEvery != "" && c.ReconcileQuery == "" {
			errs = append(errs, fmt.Errorf("%s: reconcile_every needs reconcile_query", label))
		}
		if c.ReconcileEvery != "" {
			if dur, err := time.ParseDuration(c.ReconcileEvery); err != nil || dur < time.Minute {
				errs = append(errs, fmt.Errorf("%s: reconcile_every %q must be a duration of at least 1m", label, c.ReconcileEvery))
			}
		}
		switch {
		case c.Prune && c.Kind != "kubernetes" && c.Kind != "sql" && c.Kind != "rest":
			errs = append(errs, fmt.Errorf("%s: prune is only meaningful for a full listing (kubernetes, sql or rest)", label))
		case c.Prune && c.Kind == "rest" && c.SinceParam != "":
			errs = append(errs, fmt.Errorf("%s: prune needs a full listing, but since_param asks for changes only", label))
		case c.Prune && c.Kind == "sql" && QueryHasParam(c.Query):
			errs = append(errs, fmt.Errorf("%s: prune needs a full listing, but the query filters on the last-run time; remove the parameter", label))
		}
		if c.Mapping != nil {
			errs = append(errs, validateMapping(sc, *c.Mapping, label+" mapping", false)...)
		}
	}
	vseen := map[string]bool{}
	for _, v := range d.Views {
		if _, ok := sc.Object(v.Type); !ok || v.ID == "" || vseen[v.ID] {
			errs = append(errs, fmt.Errorf("view %q: unknown type %q or duplicate id", v.ID, v.Type))
		}
		vseen[v.ID] = true
	}
	if d.RefreshInterval != "" {
		if dur, err := time.ParseDuration(d.RefreshInterval); err != nil || dur < 10*time.Second {
			errs = append(errs, fmt.Errorf("refresh_interval %q must be a duration of at least 10s", d.RefreshInterval))
		}
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

// PackFilesJob is the scheduler's name for re-reading the pack's file
// mappings; connectors may not use it.
const PackFilesJob = "pack-files"

var selectorText = regexp.MustCompile(`^[A-Za-z0-9_./=!, ()-]{0,200}$`)
var paramName = regexp.MustCompile(`^[A-Za-z$@_][A-Za-z0-9_.$@-]{0,63}$`)
var kubeName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

// CheckReadOnlyQuery refuses anything but a single SELECT or WITH statement.
// It is a guard against a mis-typed pack, not a security boundary: the
// connector also runs the query in a read-only transaction, and the database
// account should be read-only.
func CheckReadOnlyQuery(q string) error {
	t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(q), ";"))
	l := strings.ToLower(t)
	if !(strings.HasPrefix(l, "select") || strings.HasPrefix(l, "with")) {
		return errors.New("query must be a SELECT (or WITH ... SELECT)")
	}
	if strings.Contains(t, ";") {
		return errors.New("query must be a single statement")
	}
	return nil
}

func validateMapping(sc *Schema, m Mapping, label string, needSource bool) []error {
	var errs []error
	ot, ok := sc.Object(m.Type)
	if !ok {
		return []error{fmt.Errorf("%s: unknown object type %q", label, m.Type)}
	}
	if (needSource && m.Source == "") || m.Key == "" || m.Namespace == "" {
		errs = append(errs, fmt.Errorf("%s (%s): source, namespace and key are required", label, m.Type))
	}
	for p := range m.Props {
		found := false
		for _, dp := range ot.Properties {
			found = found || dp.Name == p
		}
		if !found {
			errs = append(errs, fmt.Errorf("%s (%s): unknown property %q", label, m.Type, p))
		}
	}
	for _, l := range m.Links {
		lt, ok := sc.Link(l.Type)
		if !ok || lt.From != m.Type || lt.To != l.To {
			errs = append(errs, fmt.Errorf("%s (%s): link %q must join %s to %s", label, m.Type, l.Type, m.Type, l.To))
		}
	}
	return errs
}

// QueryHasParam reports whether a SQL query takes the last-run time argument
// ($1 or ?). A query without one is a full listing.
func QueryHasParam(q string) bool {
	return strings.Contains(q, "$1") || strings.Contains(q, "?")
}
