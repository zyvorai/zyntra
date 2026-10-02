// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package draft turns sample exports and a one-line industry description
// into a pack: pack.yaml, kpis.yaml, sources.example.yaml, a README and the
// samples as the fixture. A model may propose KPIs, edges and actions as
// JSON; this package checks every proposal against the sample columns and
// writes the YAML itself. An action whose effect cites no column in the
// sample is refused. Without a model only the KPIs are drafted.
package draft

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/envx"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/pack"
)

// Sample is one export the pack will read.
type Sample struct {
	Name string `json:"name"`
	Data []byte `json:"-"`
}

// Request is what an operator gives: an industry line and samples.
type Request struct {
	Industry string   `json:"industry"`
	ID       string   `json:"id,omitempty"`
	Samples  []Sample `json:"samples"`
}

// Column is what the profile says about one sample column.
type Column struct {
	Name     string   `json:"name"`
	Numeric  bool     `json:"numeric"`
	Min      float64  `json:"min,omitempty"`
	Max      float64  `json:"max,omitempty"`
	Mean     float64  `json:"mean,omitempty"`
	Sum      float64  `json:"sum,omitempty"`
	Values   []string `json:"values,omitempty"` // distinct text values, when few
	Distinct int      `json:"distinct"`
}

// File is the profile of one sample.
type File struct {
	Name    string   `json:"name"`
	Rows    int      `json:"rows"`
	Columns []Column `json:"columns"`
}

// Profile describes every sample; it is all the model sees of the data.
type Profile struct {
	Files []File `json:"files"`
}

func (p Profile) column(file, col string) (Column, bool) {
	for _, f := range p.Files {
		if file != "" && f.Name != file {
			continue
		}
		for _, c := range f.Columns {
			if c.Name == col {
				return c, true
			}
		}
	}
	return Column{}, false
}

func (p Profile) fileOf(col string) string {
	for _, f := range p.Files {
		for _, c := range f.Columns {
			if c.Name == col {
				return f.Name
			}
		}
	}
	return ""
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)

// ProfileSamples parses each sample (CSV, JSON or YAML rows) and summarises
// its columns.
func ProfileSamples(samples []Sample) (Profile, error) {
	var p Profile
	if len(samples) == 0 {
		return p, fmt.Errorf("at least one sample is required")
	}
	for _, s := range samples {
		name := filepath.Base(s.Name)
		if !safeName.MatchString(name) {
			return p, fmt.Errorf("sample name %q: use letters, digits, dot, dash or underscore", s.Name)
		}
		format := adapters.FormatOf(name)
		if format == "prometheus" {
			return p, fmt.Errorf("%s: samples must be CSV, JSON or YAML rows", name)
		}
		doc, err := adapters.Parse(format, s.Data)
		if err != nil {
			return p, fmt.Errorf("%s: %w", name, err)
		}
		rows, ok := doc.([]any)
		if !ok || len(rows) == 0 {
			return p, fmt.Errorf("%s: expected a non-empty list of rows", name)
		}
		p.Files = append(p.Files, profileRows(name, rows))
	}
	return p, nil
}

func profileRows(name string, rows []any) File {
	type acc struct {
		nums   []float64
		texts  map[string]bool
		seen   int
		isText bool
	}
	cols := map[string]*acc{}
	var order []string
	for _, el := range rows {
		row, ok := el.(map[string]any)
		if !ok {
			continue
		}
		for k, v := range row {
			a := cols[k]
			if a == nil {
				a = &acc{texts: map[string]bool{}}
				cols[k] = a
				order = append(order, k)
			}
			a.seen++
			switch t := v.(type) {
			case float64:
				a.nums = append(a.nums, t)
			case int:
				a.nums = append(a.nums, float64(t))
			default:
				a.isText = true
				if len(a.texts) <= 12 {
					a.texts[fmt.Sprint(v)] = true
				}
			}
		}
	}
	sort.Strings(order)
	f := File{Name: name, Rows: len(rows)}
	for _, k := range order {
		a := cols[k]
		c := Column{Name: k}
		if !a.isText && len(a.nums) > 0 {
			c.Numeric = true
			c.Min, c.Max = math.Inf(1), math.Inf(-1)
			distinct := map[float64]bool{}
			for _, n := range a.nums {
				c.Min, c.Max, c.Sum = math.Min(c.Min, n), math.Max(c.Max, n), c.Sum+n
				distinct[n] = true
			}
			c.Mean = round(c.Sum / float64(len(a.nums)))
			c.Sum, c.Min, c.Max = round(c.Sum), round(c.Min), round(c.Max)
			c.Distinct = len(distinct)
		} else {
			for t := range a.texts {
				c.Values = append(c.Values, t)
			}
			sort.Strings(c.Values)
			c.Distinct = len(a.texts)
			if c.Distinct > 12 {
				c.Values = nil
			}
		}
		f.Columns = append(f.Columns, c)
	}
	return f
}

func round(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// Spec is the draft the model returns (or the heuristic builds).
type Spec struct {
	Pack struct {
		ID       string   `json:"id"`
		Title    string   `json:"title"`
		Industry string   `json:"industry"`
		Owners   []string `json:"owners"`
		Timezone string   `json:"timezone"`
	} `json:"pack"`
	KPIs    []KPISpec    `json:"kpis"`
	Edges   []EdgeSpec   `json:"edges"`
	Actions []ActionSpec `json:"actions"`
}

// KPISpec reads one column of one sample.
type KPISpec struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Unit        string            `json:"unit,omitempty"`
	UnitClass   string            `json:"unit_class,omitempty"`
	Currency    string            `json:"currency,omitempty"`
	Owner       string            `json:"owner"`
	Target      *float64          `json:"target,omitempty"`
	Direction   string            `json:"direction,omitempty"`
	Criticality string            `json:"criticality,omitempty"`
	File        string            `json:"file"`
	Column      string            `json:"column"`
	Agg         string            `json:"agg,omitempty"`
	Where       map[string]string `json:"where,omitempty"`
	// CountWhere makes the KPI the share of rows matching column=value
	// (column is then the match column).
	CountWhere string `json:"count_where,omitempty"`
	Why        string `json:"why,omitempty"`
}

// EdgeSpec is a proposed dependency.
type EdgeSpec struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Weight     float64 `json:"weight"`
	Confidence float64 `json:"confidence"`
	Why        string  `json:"why"`
}

// EffectSpec must cite the sample column that justifies it.
type EffectSpec struct {
	KPI         string  `json:"kpi"`
	Change      float64 `json:"change"`
	Uncertainty float64 `json:"uncertainty,omitempty"`
	File        string  `json:"file,omitempty"`
	Column      string  `json:"column"`
}

// ActionSpec is a webhook or file action.
type ActionSpec struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	Description  string       `json:"description,omitempty"`
	Adapter      string       `json:"adapter"`
	Risk         string       `json:"risk,omitempty"`
	Effects      []EffectSpec `json:"effects"`
	Precondition *struct {
		KPI       string  `json:"kpi"`
		WorseThan float64 `json:"worse_than"`
		Why       string  `json:"why"`
	} `json:"precondition,omitempty"`
	// Webhook: URLVar is a ZYNTRA_*_URL variable the installer binds.
	URLVar string         `json:"url_var,omitempty"`
	Path   string         `json:"path,omitempty"`
	Body   map[string]any `json:"body,omitempty"`
	// File: Content is a text/template; rows "KPI" lists the cited rows.
	Content string `json:"content,omitempty"`
}

// Result is the draft and everything that was dropped on the way.
type Result struct {
	Files      map[string]string `json:"files"`
	Refused    []string          `json:"refused,omitempty"`
	Notes      []string          `json:"notes,omitempty"`
	Mode       string            `json:"mode"` // heuristic | llm
	Model      string            `json:"model,omitempty"`
	LLMError   string            `json:"llm_error,omitempty"`
	Profile    Profile           `json:"profile"`
	Validation *pack.Report      `json:"validation,omitempty"`
}

var (
	idRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	packIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	urlVarRe = regexp.MustCompile(`^ZYNTRA_[A-Z0-9_]+_URL$`)
	slugRe   = regexp.MustCompile(`[^a-z0-9]+`)
	// A pack never decides about a person (non-negotiable).
	personDecision = regexp.MustCompile(`(?i)diagnos|prescri|treatment|medicat|loan|(deny|refuse|grant) credit|credit[ _-]?(score|limit|decision|check|rating)|creditworth|underwrit|deny (a |the )?claim|sentenc|parole|hire|hiring|dismiss|terminate (an |the )?employee|fire (an |the )?employee|eviction`)
)

func slug(s, sep string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), sep), sep)
}

// Heuristic drafts KPIs from numeric columns. It proposes no targets,
// edges or actions: those need a model or a person.
func Heuristic(req Request, p Profile) Spec {
	var s Spec
	s.Pack.Industry = req.Industry
	seen := map[string]bool{}
	for _, f := range p.Files {
		stem := slug(strings.TrimSuffix(f.Name, filepath.Ext(f.Name)), "_")
		for _, c := range f.Columns {
			if !c.Numeric || c.Distinct < 2 {
				continue
			}
			id := slug(c.Name, "_")
			if id == "" || id[0] < 'a' || id[0] > 'z' {
				id = "k_" + id
			}
			if seen[id] {
				id = stem + "_" + id
			}
			seen[id] = true
			agg := "avg"
			if regexp.MustCompile(`(?i)amount|sales|revenue|qty|quantity|units|count|total|net|gross|kwh|cost`).MatchString(c.Name) {
				agg = "sum"
			}
			s.KPIs = append(s.KPIs, KPISpec{ID: id, Name: strings.ReplaceAll(c.Name, "_", " "), Owner: "owner", File: f.Name, Column: c.Name, Agg: agg})
		}
	}
	return s
}

const draftPrompt = `You draft a Zyntra pack: KPIs, dependency edges and actions for one site, from a profile of sample exports.
Return one JSON object with keys pack, kpis, edges, actions, matching the SCHEMA. Rules:
- Every KPI reads exactly one column of one profiled file ("file", "column"). Use "agg" (sum|avg|max|min) for numeric columns, or "count_where" ("value") with "column" set to a text column to make the KPI the share of rows with that value. Never invent a column.
- Targets are suggestions a person will edit; use the profile's ranges, never made-up figures. Leave target out if unsure.
- Edges: only between your KPI ids, weight between -1 and 1, confidence between 0.1 and 0.6, and a one-line "why".
- Actions: adapter "webhook" or "file" only. Every effect names the KPI it moves and cites the profiled column ("column", "file") that shows the lever. Effects are relative changes between -0.6 and 0.6.
- Webhook actions name a url_var like ZYNTRA_POS_URL (the installer binds it) and a path; body values may be literals, "gap:<kpi id>" or "rows:<kpi id>".
- File actions give a markdown "content"; it may use {{range rows "<kpi id>"}}{{.column_name}}{{end}} to list the rows behind a KPI.
- No action may decide anything about a person: no diagnosis, treatment, credit, hiring, dismissal, legal outcome. Levers are capacity, queues, stock, price lists, schedules and routes.
- ids are lower_snake_case. owners are short role names (floor, buyer, plant, ops).
SCHEMA:
{"pack":{"id":"kebab-id","title":"...","industry":"...","owners":["..."],"timezone":"Asia/Kolkata"},
 "kpis":[{"id":"...","name":"...","unit":"...","unit_class":"percent|count|currency|duration|ratio","currency":"INR","owner":"...","target":0,"direction":"lower|higher","criticality":"low|medium|high","file":"...","column":"...","agg":"avg","count_where":"","why":"..."}],
 "edges":[{"from":"...","to":"...","weight":0.3,"confidence":0.3,"why":"..."}],
 "actions":[{"id":"...","title":"...","description":"...","adapter":"webhook|file","risk":"low|medium|high","effects":[{"kpi":"...","change":-0.2,"uncertainty":0.3,"file":"...","column":"..."}],"precondition":{"kpi":"...","worse_than":0,"why":"..."},"url_var":"ZYNTRA_X_URL","path":"/...","body":{},"content":"..."}]}`

// Draft profiles the samples, asks the model when one is configured, and
// builds the pack files. It never writes to disk.
func Draft(ctx context.Context, llm *ai.Provider, req Request) (Result, error) {
	prof, err := ProfileSamples(req.Samples)
	if err != nil {
		return Result{}, err
	}
	res := Result{Mode: "heuristic", Profile: prof}
	spec := Heuristic(req, prof)
	if llm != nil {
		pj, _ := json.Marshal(prof)
		text, err := llm.Chat(ctx, draftPrompt, "INDUSTRY: "+req.Industry+"\n\nPROFILE:\n"+string(pj), true)
		var got Spec
		if err == nil {
			err = json.Unmarshal([]byte(text), &got)
		}
		if err != nil {
			res.LLMError = err.Error()
			res.Notes = append(res.Notes, "the model did not return a usable draft; KPIs were drafted from numeric columns only")
		} else {
			spec, res.Mode, res.Model = got, "llm", llm.Model
		}
	} else {
		res.Notes = append(res.Notes, "no model configured (ZYNTRA_AI_BASE_URL): KPIs were drafted from numeric columns; targets, edges and actions are left to you")
	}
	res.Files, res.Refused, res.Notes = Build(req, prof, spec, res.Notes)
	return res, nil
}

type kpiOut struct {
	ID          string        `yaml:"id"`
	Name        string        `yaml:"name"`
	Unit        string        `yaml:"unit,omitempty"`
	UnitClass   string        `yaml:"unitClass,omitempty"`
	Currency    string        `yaml:"currency,omitempty"`
	Owner       string        `yaml:"owner"`
	Value       float64       `yaml:"value"`
	Target      *float64      `yaml:"target,omitempty"`
	Direction   string        `yaml:"direction,omitempty"`
	Criticality string        `yaml:"criticality,omitempty"`
	Source      *graph.Source `yaml:"source"`
}

type actionOut struct {
	ID            string               `yaml:"id"`
	Name          string               `yaml:"name"`
	Description   string               `yaml:"description,omitempty"`
	Adapter       string               `yaml:"adapter"`
	Risk          string               `yaml:"risk"`
	Preconditions []graph.Precondition `yaml:"preconditions,omitempty"`
	Effects       []effectOut          `yaml:"effects"`
	Webhook       *graph.Webhook       `yaml:"webhook,omitempty"`
	File          *graph.FileOut       `yaml:"file,omitempty"`
}

type effectOut struct {
	KPI         string  `yaml:"kpi"`
	Change      float64 `yaml:"change"`
	Uncertainty float64 `yaml:"uncertainty,omitempty"`
}

type modelOut struct {
	Name    string       `yaml:"name"`
	KPIs    []kpiOut     `yaml:"kpis"`
	Edges   []graph.Edge `yaml:"edges,omitempty"`
	Actions []actionOut  `yaml:"actions,omitempty"`
}

var tplFuncs = template.FuncMap{
	"rows": func(string) []map[string]any { return nil },
	"fill": func(string) string { return "" },
	"pct":  func(float64) string { return "" },
	"num":  func(float64) string { return "" },
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// Build checks a spec against the profile and renders the pack files.
func Build(req Request, prof Profile, s Spec, notes []string) (files map[string]string, refused, outNotes []string) {
	refuse := func(f string, a ...any) { refused = append(refused, fmt.Sprintf(f, a...)) }
	id := req.ID
	if id == "" {
		id = s.Pack.ID
	}
	if id == "" {
		id = slug(req.Industry, "-")
	}
	if !packIDRe.MatchString(id) {
		id = "draft"
	}
	title := s.Pack.Title
	if title == "" {
		title = req.Industry
	}
	tz := s.Pack.Timezone
	if tz == "" {
		tz = "UTC"
	}

	kpis := map[string]bool{}
	owners := map[string]bool{}
	var m modelOut
	m.Name = id
	for _, k := range s.KPIs {
		if !idRe.MatchString(k.ID) || kpis[k.ID] {
			refuse("kpi %q: id must be unique lower_snake_case", k.ID)
			continue
		}
		file := k.File
		if file == "" {
			file = prof.fileOf(k.Column)
		}
		col, ok := prof.column(file, k.Column)
		if !ok {
			refuse("kpi %s: column %q is not in %s", k.ID, k.Column, orAny(file))
			continue
		}
		src := &graph.Source{Kind: graph.SourceFile, File: "fixture/" + file, Where: k.Where}
		for wc := range k.Where {
			if _, ok := prof.column(file, wc); !ok {
				refuse("kpi %s: where column %q is not in %s", k.ID, wc, file)
				src = nil
				break
			}
		}
		if src == nil {
			continue
		}
		switch {
		case k.CountWhere != "":
			src.Field, src.Denominator = fmt.Sprintf("#(%s=%s)", col.Name, k.CountWhere), "#"
		case col.Numeric:
			agg := k.Agg
			switch agg {
			case "sum", "avg", "max", "min":
			default:
				agg = "avg"
			}
			src.Field, src.Agg = "*."+col.Name, agg
		default:
			refuse("kpi %s: column %q is text; use count_where", k.ID, k.Column)
			continue
		}
		owner := slug(k.Owner, "-")
		if owner == "" {
			owner = "owner"
		}
		dir := ""
		switch k.Direction {
		case "lower", "higher":
			dir = k.Direction
		}
		if k.Target != nil && dir == "" {
			dir = "lower"
		}
		crit := ""
		switch k.Criticality {
		case "low", "medium", "high":
			crit = k.Criticality
		}
		uc := ""
		switch k.UnitClass {
		case "percent", "count", "currency", "duration", "ratio":
			uc = k.UnitClass
		}
		if k.CountWhere != "" {
			uc = "ratio"
		}
		name := k.Name
		if name == "" {
			name = strings.ReplaceAll(k.ID, "_", " ")
		}
		cur := ""
		if regexp.MustCompile(`^[A-Z]{3}$`).MatchString(k.Currency) {
			cur, uc = k.Currency, "currency"
		}
		kpis[k.ID], owners[owner] = true, true
		m.KPIs = append(m.KPIs, kpiOut{ID: k.ID, Name: name, Unit: k.Unit, UnitClass: uc, Currency: cur, Owner: owner,
			Target: k.Target, Direction: dir, Criticality: crit, Source: src})
	}
	if len(m.KPIs) == 0 {
		notes = append(notes, "no KPI could be drafted from the samples")
	}

	for _, e := range s.Edges {
		if !kpis[e.From] || !kpis[e.To] || e.From == e.To {
			refuse("edge %s -> %s: both ends must be drafted KPIs", e.From, e.To)
			continue
		}
		if e.Why == "" {
			refuse("edge %s -> %s: no why", e.From, e.To)
			continue
		}
		m.Edges = append(m.Edges, graph.Edge{From: e.From, To: e.To, Weight: round(clamp(e.Weight, -1, 1)),
			Confidence: round(clamp(e.Confidence, 0.1, 0.6)), Why: "draft: " + e.Why})
	}

	vars := map[string]bool{}
	acts := map[string]bool{}
	for _, a := range s.Actions {
		if !idRe.MatchString(a.ID) || acts[a.ID] || kpis[a.ID] {
			refuse("action %q: id must be unique lower_snake_case", a.ID)
			continue
		}
		if personDecision.MatchString(a.ID + " " + a.Title + " " + a.Description) {
			refuse("action %s: decides about a person (clinical, legal, credit or employment); packs only move capacity, queues, stock, prices, schedules and routes", a.ID)
			continue
		}
		var effects []effectOut
		bad := ""
		for _, e := range a.Effects {
			file := e.File
			if file == "" {
				file = prof.fileOf(e.Column)
			}
			if e.Column == "" {
				bad = fmt.Sprintf("effect on %s cites no column in the sample", e.KPI)
				break
			}
			if _, ok := prof.column(file, e.Column); !ok {
				bad = fmt.Sprintf("effect on %s cites column %q, which is not in %s", e.KPI, e.Column, orAny(file))
				break
			}
			if !kpis[e.KPI] {
				bad = fmt.Sprintf("effect on %q: not a drafted KPI", e.KPI)
				break
			}
			effects = append(effects, effectOut{KPI: e.KPI, Change: round(clamp(e.Change, -0.6, 0.6)), Uncertainty: round(clamp(e.Uncertainty, 0, 0.6))})
		}
		if bad == "" && len(effects) == 0 {
			bad = "no effects"
		}
		if bad != "" {
			refuse("action %s: %s", a.ID, bad)
			continue
		}
		risk := a.Risk
		switch risk {
		case "low", "medium", "high":
		default:
			risk = "medium"
		}
		out := actionOut{ID: a.ID, Name: orStr(a.Title, a.ID), Description: a.Description, Adapter: a.Adapter, Risk: risk, Effects: effects}
		if pc := a.Precondition; pc != nil && kpis[pc.KPI] {
			w := pc.WorseThan
			out.Preconditions = []graph.Precondition{{KPI: pc.KPI, WorseThan: &w, Why: pc.Why}}
		}
		switch a.Adapter {
		case graph.KindWebhook:
			if !urlVarRe.MatchString(a.URLVar) || !envx.Allowed(a.URLVar) {
				refuse("action %s: url_var %q must be a ZYNTRA_*_URL variable", a.ID, a.URLVar)
				continue
			}
			path := "/" + strings.TrimLeft(a.Path, "/")
			if strings.ContainsAny(path, " ?#{}$") {
				refuse("action %s: path %q must be a plain path", a.ID, a.Path)
				continue
			}
			body, why := checkBody(a.Body, kpis)
			if why != "" {
				refuse("action %s: %s", a.ID, why)
				continue
			}
			vars[a.URLVar] = true
			out.Webhook = &graph.Webhook{Method: "POST", URL: "${" + a.URLVar + "}" + path, Body: body}
		case graph.KindFile:
			content := a.Content
			if content == "" || !parses(content) {
				content = defaultContent(out, effects)
			}
			out.File = &graph.FileOut{Path: a.ID + "/{{.Stamp}}.md", Content: content}
		default:
			refuse("action %s: adapter must be webhook or file, not %q", a.ID, a.Adapter)
			continue
		}
		acts[a.ID] = true
		m.Actions = append(m.Actions, out)
	}

	files = map[string]string{}
	ownerList := sortedKeys(owners)
	for _, o := range s.Pack.Owners {
		if o = slug(o, "-"); o != "" && !owners[o] {
			ownerList = append(ownerList, o)
		}
	}
	man := pack.Pack{ID: id, Title: title, Industry: orStr(s.Pack.Industry, req.Industry), Version: "0.1.0", Owners: ownerList, Timezone: tz}
	files[pack.Manifest] = header("Drafted by Zyntra for: "+oneLine(req.Industry)) + mustYAML(man)
	files[pack.ModelFile] = header("DRAFT. Weights, targets and effects are suggestions: edit them, then run\n# zyntra pack validate on this directory before a pilot.") + mustYAML(m)

	var src strings.Builder
	src.WriteString(header("What to bind before a pilot. Dry-run never needs the variables."))
	src.WriteString("variables:")
	if len(vars) == 0 {
		src.WriteString(" []\n")
	} else {
		src.WriteString("\n")
		for _, v := range sortedKeys(vars) {
			fmt.Fprintf(&src, "  - name: %s\n    description: base URL the webhook actions post to\n    example: https://example.internal/api\n", v)
		}
	}
	src.WriteString("files:\n")
	for _, f := range prof.Files {
		var cols []string
		for _, c := range f.Columns {
			cols = append(cols, c.Name)
		}
		fmt.Fprintf(&src, "  - path: fixture/%s\n    description: %q\n", f.Name, "sample export: "+strings.Join(cols, ", "))
	}
	files[pack.SourcesExample] = src.String()
	files[pack.Readme] = readme(id, title, req, prof, m, refused)
	for _, smp := range req.Samples {
		files["fixture/"+filepath.Base(smp.Name)] = string(smp.Data)
	}
	return files, refused, notes
}

func checkBody(body map[string]any, kpis map[string]bool) (map[string]any, string) {
	var walk func(v any) string
	walk = func(v any) string {
		switch t := v.(type) {
		case string:
			if strings.Contains(t, "${") {
				return "body must not reference variables"
			}
			for _, pre := range []string{"gap:", "kpi:", "rows:"} {
				if id, ok := strings.CutPrefix(t, pre); ok && !kpis[id] {
					return fmt.Sprintf("body references %s%s, which is not a drafted KPI", pre, id)
				}
			}
		case map[string]any:
			for _, x := range t {
				if w := walk(x); w != "" {
					return w
				}
			}
		case []any:
			for _, x := range t {
				if w := walk(x); w != "" {
					return w
				}
			}
		}
		return ""
	}
	if w := walk(body); w != "" {
		return nil, w
	}
	return body, ""
}

func parses(content string) bool {
	_, err := template.New("c").Funcs(tplFuncs).Parse(content)
	return err == nil
}

func defaultContent(a actionOut, effects []effectOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\nRaised by Zyntra on {{.Date}} at {{.Time}} after approval.\n\n", a.Name)
	for _, e := range effects {
		fmt.Fprintf(&b, "- %s: {{num (index .KPIs %q).Value}}\n", e.KPI, e.KPI)
	}
	return b.String()
}

func readme(id, title string, req Request, prof Profile, m modelOut, refused []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (draft)\n\nDrafted by `zyntra pack draft` for \"%s\" from %d sample file(s). Every weight, target and effect is a suggestion: edit `kpis.yaml`, then run `zyntra pack validate packs/%s`.\n\n", title, oneLine(req.Industry), len(prof.Files), id)
	b.WriteString("## KPIs and the column each reads\n\n| KPI | File | Field | Owner | Target |\n| --- | --- | --- | --- | --- |\n")
	for _, k := range m.KPIs {
		t := "set one"
		if k.Target != nil {
			t = fmt.Sprintf("%g (draft)", *k.Target)
		}
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s | %s |\n", k.ID, k.Source.File, k.Source.Field, k.Owner, t)
	}
	if len(m.Actions) > 0 {
		b.WriteString("\n## Actions\n\n")
		for _, a := range m.Actions {
			fmt.Fprintf(&b, "- `%s` (%s, %s risk): %s\n", a.ID, a.Adapter, a.Risk, a.Name)
		}
	}
	if len(refused) > 0 {
		b.WriteString("\n## Refused while drafting\n\n")
		for _, r := range refused {
			b.WriteString("- " + r + "\n")
		}
	}
	b.WriteString("\n## Before a pilot\n\n1. Set every target you care about; KPIs without one are watched, not scored.\n2. Check each edge's weight and why against what you know.\n3. Keep actions on dry-run until the gaps and plan look right for a week.\n")
	return b.String()
}

// Write saves a draft to dir, which must not exist yet.
func Write(dir string, files map[string]string) error {
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists; pick a new directory", dir)
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Validate writes the draft to a scratch directory and runs pack validate.
func Validate(ctx context.Context, files map[string]string) (*pack.Report, error) {
	tmp, err := os.MkdirTemp("", "zyntra-draft-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "pack")
	if err := Write(dir, files); err != nil {
		return nil, err
	}
	r := pack.Validate(ctx, dir)
	r.Path = ""
	return &r, nil
}

func header(text string) string {
	return "# Copyright 2026 Zyvor AI Labs · https://zyvor.dev\n# SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0\n#\n# " + text + "\n"
}

func mustYAML(v any) string {
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return "# yaml: " + err.Error() + "\n"
	}
	return b.String()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func orAny(file string) string {
	if file == "" {
		return "any sample"
	}
	return file
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
