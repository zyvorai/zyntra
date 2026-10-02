// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package pack loads industry packs: a directory holding pack.yaml (id,
// owners, timezone, calendars), kpis.yaml (the model), sources.example.yaml
// (the variables and files the pack reads) and a README. The engine never
// imports a pack by name; a pack is only files.
package pack

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/calendar"
	"github.com/zyvorai/zyntra/internal/envx"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
)

// File names inside a pack directory.
const (
	Manifest       = "pack.yaml"
	ModelFile      = "kpis.yaml"
	SourcesExample = "sources.example.yaml"
	Readme         = "README.md"
)

// Pack is pack.yaml.
type Pack struct {
	ID        string       `yaml:"id"`
	Title     string       `yaml:"title"`
	Industry  string       `yaml:"industry,omitempty"`
	Version   string       `yaml:"version,omitempty"`
	Owners    []string     `yaml:"owners,omitempty"`
	Timezone  string       `yaml:"timezone,omitempty"`
	Calendar  string       `yaml:"calendar,omitempty"`
	Calendars calendar.Set `yaml:"calendars,omitempty"`
}

// LoadOntology reads the pack's ontology.yaml and checks it against the
// model. It returns nil, nil for a pack with no ontology or a plain model
// file. The second result is the pack directory the mappings are relative to.
func LoadOntology(path string, m *graph.Model) (*ontology.Definition, string, error) {
	dir := path
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		dir = filepath.Dir(path)
	}
	d, err := ontology.LoadDefinition(dir)
	if err != nil || d == nil {
		return nil, dir, err
	}
	if m != nil {
		err := d.CheckModel(func(id string) bool { _, ok := m.KPI(id); return ok },
			func(id string) bool { _, ok := m.Action(id); return ok })
		if err != nil {
			return nil, dir, fmt.Errorf("%s: %w", ontology.FileName, err)
		}
	}
	return d, dir, nil
}

// Sources is sources.example.yaml: what an installer must bind.
type Sources struct {
	Variables []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description,omitempty"`
		Example     string `yaml:"example,omitempty"`
	} `yaml:"variables,omitempty"`
	Files []struct {
		Path        string `yaml:"path"`
		Description string `yaml:"description,omitempty"`
	} `yaml:"files,omitempty"`
}

var packID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func decodeStrict(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// ReadManifest reads pack.yaml from dir; it returns nil, nil when there is
// none.
func ReadManifest(dir string) (*Pack, error) {
	path := filepath.Join(dir, Manifest)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var p Pack
	if err := decodeStrict(path, &p); err != nil {
		return nil, err
	}
	if !packID.MatchString(p.ID) {
		return nil, fmt.Errorf("%s: id must be lowercase letters, digits and dashes", path)
	}
	if p.Title == "" {
		return nil, fmt.Errorf("%s: title is required", path)
	}
	return &p, nil
}

// Load reads a model from a pack directory, or from a model file whose
// directory holds pack.yaml, applying the pack's owners, timezone and
// calendars. A plain model file without pack.yaml loads as before.
func Load(path string) (*graph.Model, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	dir, file := filepath.Dir(path), path
	if fi.IsDir() {
		dir, file = path, filepath.Join(path, ModelFile)
	}
	p, err := ReadManifest(dir)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if fi.IsDir() {
			return nil, fmt.Errorf("%s: not a pack (no %s)", path, Manifest)
		}
		return graph.Load(file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	m, err := graph.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	m.Dir = filepath.Dir(file)
	if err := apply(m, p); err != nil {
		return nil, fmt.Errorf("pack %s: %w", p.ID, err)
	}
	return m, nil
}

func apply(m *graph.Model, p *Pack) error {
	m.Pack = &graph.PackInfo{ID: p.ID, Title: p.Title, Industry: p.Industry, Version: p.Version, Owners: p.Owners}
	if m.Timezone == "" {
		m.Timezone = p.Timezone
	}
	if m.Calendar == "" {
		m.Calendar = p.Calendar
	}
	if len(p.Calendars) > 0 {
		if m.Calendars == nil {
			m.Calendars = calendar.Set{}
		}
		for name, w := range p.Calendars {
			if _, ok := m.Calendars[name]; !ok {
				m.Calendars[name] = w
			}
		}
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if len(p.Owners) > 0 {
		known := map[string]bool{}
		for _, o := range p.Owners {
			known[o] = true
		}
		for _, k := range m.KPIs {
			if k.Owner != "" && !known[k.Owner] {
				return fmt.Errorf("kpi %s: owner %q is not one of the pack owners (%s)", k.ID, k.Owner, strings.Join(p.Owners, ", "))
			}
		}
	}
	return nil
}

// Summary is one line of `zyntra pack list`.
type Summary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Industry string `json:"industry,omitempty"`
	Version  string `json:"version,omitempty"`
	Dir      string `json:"dir"`
	KPIs     int    `json:"kpis"`
	Actions  int    `json:"actions"`
	Error    string `json:"error,omitempty"`
}

// List returns the packs directly under root, sorted by id.
func List(root string) ([]Summary, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, Manifest)); err != nil {
			continue
		}
		s := Summary{ID: e.Name(), Dir: dir}
		m, err := Load(dir)
		if err != nil {
			s.Error = err.Error()
		} else {
			s.ID, s.Title, s.Industry, s.Version = m.Pack.ID, m.Pack.Title, m.Pack.Industry, m.Pack.Version
			s.KPIs, s.Actions = len(m.KPIs), len(m.Actions)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Report is the result of validating a pack.
type Report struct {
	Path     string   `json:"path"`
	ID       string   `json:"id,omitempty"`
	OK       []string `json:"ok"`
	Warnings []string `json:"warnings,omitempty"`
	Errors   []string `json:"errors,omitempty"`
}

func (r *Report) okf(f string, a ...any)   { r.OK = append(r.OK, fmt.Sprintf(f, a...)) }
func (r *Report) warnf(f string, a ...any) { r.Warnings = append(r.Warnings, fmt.Sprintf(f, a...)) }
func (r *Report) errf(f string, a ...any)  { r.Errors = append(r.Errors, fmt.Sprintf(f, a...)) }

// Valid reports whether the pack has no errors.
func (r Report) Valid() bool { return len(r.Errors) == 0 }

// Validate checks a pack directory: the model loads, owners and calendars
// resolve, every ${ZYNTRA_*} variable is documented, file sources parse,
// and every action simulates and renders in dry-run.
func Validate(ctx context.Context, dir string) Report {
	r := Report{Path: dir}
	m, err := Load(dir)
	if err != nil {
		r.errf("%v", err)
		return r
	}
	r.ID = m.Pack.ID
	r.okf("model loads: %d KPIs, %d edges, %d actions", len(m.KPIs), len(m.Edges), len(m.Actions))
	if _, err := os.Stat(filepath.Join(dir, Readme)); err != nil {
		r.warnf("no %s describing the gaps this pack is judged on", Readme)
	}

	refs := modelRefs(m)
	documented := map[string]bool{}
	files := map[string]bool{}
	srcPath := filepath.Join(dir, SourcesExample)
	if _, err := os.Stat(srcPath); err == nil {
		var s Sources
		if err := decodeStrict(srcPath, &s); err != nil {
			r.errf("%v", err)
		}
		for _, v := range s.Variables {
			documented[v.Name] = true
		}
		for _, f := range s.Files {
			files[f.Path] = true
		}
	} else if len(refs) > 0 {
		r.errf("the pack uses %s but has no %s", strings.Join(refs, ", "), SourcesExample)
	}
	for _, v := range refs {
		switch {
		case !strings.HasPrefix(v, envx.Prefix):
			r.errf("${%s}: only ${%s*} variables are expanded", v, envx.Prefix)
		case !envx.Allowed(v):
			r.errf("${%s} is a Zyntra credential and is never expanded in a pack", v)
		case !documented[v]:
			r.errf("${%s} is used but not documented in %s", v, SourcesExample)
		}
	}
	if len(refs) > 0 && r.Valid() {
		r.okf("variables documented: %s", strings.Join(refs, ", "))
	}

	probe := m.Clone()
	rep, _ := adapters.Refresh(ctx, probe, adapters.Config{Files: adapters.NewFileCache()})
	nfiles := 0
	for _, k := range m.KPIs {
		if k.Source == nil || k.Source.Kind != graph.SourceFile {
			continue
		}
		nfiles++
		if u := rep.KPIs[k.ID]; !u.OK {
			r.errf("kpi %s: file source %s: %s", k.ID, k.Source.File, u.Error)
		}
		if len(files) > 0 && !files[k.Source.File] {
			r.warnf("kpi %s: file %s is not listed in %s", k.ID, k.Source.File, SourcesExample)
		}
	}
	if nfiles > 0 {
		r.okf("%d file-sourced KPIs read from the fixture", nfiles)
	}

	for _, a := range probe.Actions {
		if _, err := sim.ApplyPlan(probe, []graph.Action{a}, sim.Options{}); err != nil {
			r.errf("action %s: simulate: %v", a.ID, err)
		}
		if a.Kind() == "" {
			continue
		}
		if _, err := executor.RenderIn(probe, a); err != nil {
			r.errf("action %s: render: %v", a.ID, err)
		}
	}
	r.checkOntology(dir, m, files)
	if res, err := planner.PlanWith(probe, planner.Options{}); err != nil {
		r.errf("plan: %v", err)
	} else {
		r.okf("plan: %d ranked, %d blocked", len(res.Recommendations), len(res.Blocked))
	}
	return r
}

// modelRefs lists the variables referenced by sources and webhooks.
func modelRefs(m *graph.Model) []string {
	var s []string
	for _, k := range m.KPIs {
		if k.Source == nil {
			continue
		}
		s = append(s, k.Source.URL)
		for _, v := range k.Source.Headers {
			s = append(s, v)
		}
	}
	for _, a := range m.Actions {
		if a.Webhook == nil {
			continue
		}
		s = append(s, a.Webhook.URL)
		for _, v := range a.Webhook.Headers {
			s = append(s, v)
		}
	}
	return envx.Refs(s...)
}

// checkOntology validates ontology.yaml, reads every mapped file into a
// throwaway store and reports what it would create.
func (r *Report) checkOntology(dir string, m *graph.Model, listed map[string]bool) {
	d, _, err := LoadOntology(dir, m)
	if err != nil {
		r.errf("%v", err)
		return
	}
	if d == nil {
		return
	}
	r.okf("ontology: %d object types, %d link types, %d typed actions, %d views", len(d.Objects), len(d.Links), len(d.Actions), len(d.Views))
	for _, mp := range d.Mappings {
		if len(listed) > 0 && !listed[mp.Source] {
			r.warnf("ontology file %s is not listed in %s", mp.Source, SourcesExample)
		}
	}
	st, err := ontology.Open("", d.Schema())
	if err != nil {
		r.errf("%v", err)
		return
	}
	files := adapters.NewFileCache()
	reps, err := st.IngestMappings(d, dir, "pack validate", files.Load, time.Now())
	if err != nil {
		r.errf("ontology: %v", err)
		return
	}
	for _, rep := range reps {
		for _, s := range rep.Skipped {
			r.errf("ontology: %s", s)
		}
		r.okf("ontology fixture: %d objects, %d links, %d identity candidates", rep.Objects, rep.Links, rep.Candidates)
	}
}
