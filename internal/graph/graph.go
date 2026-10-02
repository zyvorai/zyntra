// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package graph holds the KPI dependency model: KPIs, weighted edges between
// them, and the candidate actions that can move them.
package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration written as "15m" in YAML and JSON.
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	return d.parse(s)
}

func (d *Duration) parse(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	if v < 0 {
		return fmt.Errorf("duration %q must not be negative", s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	if d == 0 {
		return []byte(`""`), nil
	}
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		return d.parse(s)
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("duration must be a string like \"15m\"")
	}
	*d = Duration(time.Duration(n) * time.Second)
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	if d == 0 {
		return "", nil
	}
	return time.Duration(d).String(), nil
}

// Criticality says how much the business cares about a KPI. It weights gap
// severity and makes critical targets hard constraints.
type Criticality string

const (
	CriticalityCritical Criticality = "critical"
	CriticalityHigh     Criticality = "high"
	CriticalityNormal   Criticality = "normal"
	CriticalityLow      Criticality = "low"
)

var criticalityWeight = map[Criticality]float64{
	"":                  1,
	CriticalityCritical: 4,
	CriticalityHigh:     2,
	CriticalityNormal:   1,
	CriticalityLow:      0.5,
}

// Freshness limits how old a live value may be before it counts as stale.
type Freshness struct {
	MaxAge Duration `yaml:"maxAge,omitempty" json:"max_age,omitempty"`
	// Required blocks execution of any decision that depends on this KPI
	// while it is stale.
	Required bool `yaml:"required,omitempty" json:"required,omitempty"`
}

// Constraint is a hard limit the planner never trades against improvement.
type Constraint struct {
	KPI     string   `yaml:"kpi" json:"kpi"`
	Floor   *float64 `yaml:"floor,omitempty" json:"floor,omitempty"`
	Ceiling *float64 `yaml:"ceiling,omitempty" json:"ceiling,omitempty"`
	// MustNotWorsen forbids any move in the unhealthy direction.
	MustNotWorsen bool   `yaml:"mustNotWorsen,omitempty" json:"must_not_worsen,omitempty"`
	Why           string `yaml:"why,omitempty" json:"why,omitempty"`
	// Implicit marks constraints derived from critical KPI targets.
	Implicit bool `yaml:"-" json:"implicit,omitempty"`
}

// EffectMode says whether Change is a fraction of the current value or an
// amount in the KPI's unit.
type EffectMode string

const (
	EffectRelative EffectMode = "relative"
	EffectAbsolute EffectMode = "absolute"
)

// Provenance says where an edge weight came from.
type Provenance string

const (
	ProvenanceDeclared Provenance = "declared"
	ProvenanceLearned  Provenance = "learned"
)

// Outcome says how to judge whether an executed action worked.
type Outcome struct {
	// Window is how long to observe after execution (default 15m).
	Window Duration `yaml:"window,omitempty" json:"window,omitempty"`
	// Samples is how many consecutive fresh samples must meet the success
	// criteria (default 2).
	Samples int         `yaml:"samples,omitempty" json:"samples,omitempty"`
	Success []Criterion `yaml:"successCriteria,omitempty" json:"success_criteria,omitempty"`
	// Guardrails are KPIs that must not get worse than Tolerance.
	Guardrails []string `yaml:"guardrails,omitempty" json:"guardrails,omitempty"`
	// Tolerance is the relative worsening allowed on a guardrail (default 0.05).
	Tolerance float64 `yaml:"tolerance,omitempty" json:"tolerance,omitempty"`
}

// Criterion compares a KPI with a value. Op "met" means the KPI meets its target.
type Criterion struct {
	KPI   string   `yaml:"kpi" json:"kpi"`
	Op    string   `yaml:"op" json:"op"`
	Value *float64 `yaml:"value,omitempty" json:"value,omitempty"`
}

var criterionOps = map[string]bool{"<": true, "<=": true, ">": true, ">=": true, "met": true}

// Holds reports whether v satisfies c for kpi k.
func (c Criterion) Holds(k KPI, v float64) bool {
	if c.Op == "met" {
		return k.Target == nil || k.Meets(v)
	}
	if c.Value == nil {
		return false
	}
	t := *c.Value
	switch c.Op {
	case "<":
		return v < t
	case "<=":
		return v <= t
	case ">":
		return v > t
	case ">=":
		return v >= t
	}
	return false
}

// ActionPolicy overrides the approval policy for one action.
type ActionPolicy struct {
	Approvals int `yaml:"approvals,omitempty" json:"approvals,omitempty"`
	// Keep is required, preferred or off.
	Keep         string   `yaml:"keep,omitempty" json:"keep,omitempty"`
	RequireFresh *bool    `yaml:"requireFresh,omitempty" json:"require_fresh,omitempty"`
	Windows      []string `yaml:"maintenanceWindows,omitempty" json:"maintenance_windows,omitempty"`
}

var keepModes = map[string]bool{"": true, "required": true, "preferred": true, "off": true}

// ValidKeepMode reports whether s is a Keep policy value.
func ValidKeepMode(s string) bool { return keepModes[s] }

// Direction says which way a KPI should move to be healthier.
type Direction string

const (
	HigherIsBetter Direction = "higher"
	LowerIsBetter  Direction = "lower"
)

// Source binds a KPI to a live data source. Empty Kind means the value in the
// model file is used as-is.
//
// Kinds: prometheus (Query), kubernetes (Metric), metrics (scrape a Prometheus
// text endpoint: Endpoint, Path, Metric, Labels, Agg), and json or the
// aliases gravia|netra|fabric (Endpoint, Path, Field).
type Source struct {
	Kind     string            `yaml:"kind" json:"kind"`
	Query    string            `yaml:"query,omitempty" json:"query,omitempty"`
	Metric   string            `yaml:"metric,omitempty" json:"metric,omitempty"`
	Endpoint string            `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Path     string            `yaml:"path,omitempty" json:"path,omitempty"`
	Field    string            `yaml:"field,omitempty" json:"field,omitempty"`
	Labels   map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Agg      string            `yaml:"agg,omitempty" json:"agg,omitempty"`
	Scale    float64           `yaml:"scale,omitempty" json:"scale,omitempty"`
	// Rate turns a cumulative counter into a per-second rate between refreshes.
	Rate bool `yaml:"rate,omitempty" json:"rate,omitempty"`
}

type KPI struct {
	ID          string      `yaml:"id" json:"id"`
	Name        string      `yaml:"name" json:"name"`
	Unit        string      `yaml:"unit,omitempty" json:"unit,omitempty"`
	Owner       string      `yaml:"owner,omitempty" json:"owner,omitempty"`
	Value       float64     `yaml:"value" json:"value"`
	Target      *float64    `yaml:"target,omitempty" json:"target,omitempty"`
	Direction   Direction   `yaml:"direction,omitempty" json:"direction,omitempty"`
	Criticality Criticality `yaml:"criticality,omitempty" json:"criticality,omitempty"`
	Min         *float64    `yaml:"min,omitempty" json:"min,omitempty"`
	Max         *float64    `yaml:"max,omitempty" json:"max,omitempty"`
	Freshness   *Freshness  `yaml:"freshness,omitempty" json:"freshness,omitempty"`
	Source      *Source     `yaml:"source,omitempty" json:"source,omitempty"`
}

// Weight is the criticality multiplier applied to this KPI's gap severity.
func (k KPI) Weight() float64 { return criticalityWeight[k.Criticality] }

// Live reports whether the KPI is refreshed from a source.
func (k KPI) Live() bool { return k.Source != nil && k.Source.Kind != "" }

// Meets reports whether v meets the KPI target (true without a target).
func (k KPI) Meets(v float64) bool {
	if k.Target == nil {
		return true
	}
	switch k.Direction {
	case HigherIsBetter:
		return v >= *k.Target
	case LowerIsBetter:
		return v <= *k.Target
	}
	return true
}

// Worse reports whether moving from before to after is a move in the
// unhealthy direction. KPIs without a direction never get worse.
func (k KPI) Worse(before, after float64) bool {
	switch k.Direction {
	case HigherIsBetter:
		return after < before
	case LowerIsBetter:
		return after > before
	}
	return false
}

// Clamp bounds v to [Min, Max].
func (k KPI) Clamp(v float64) float64 {
	if k.Min != nil && v < *k.Min {
		v = *k.Min
	}
	if k.Max != nil && v > *k.Max {
		v = *k.Max
	}
	return v
}

// Edge means: a relative change of x in From causes a relative change of
// Weight*x in To.
type Edge struct {
	From   string  `yaml:"from" json:"from"`
	To     string  `yaml:"to" json:"to"`
	Weight float64 `yaml:"weight" json:"weight"`
	Why    string  `yaml:"why,omitempty" json:"why,omitempty"`
	// Confidence is the +/- fraction of Weight the modeller is unsure about
	// (0.3 means the true weight is within 70%..130% of Weight).
	Confidence float64    `yaml:"confidence,omitempty" json:"confidence,omitempty"`
	Provenance Provenance `yaml:"provenance,omitempty" json:"provenance,omitempty"`
	Delay      Duration   `yaml:"delay,omitempty" json:"delay,omitempty"`
}

// Effect is a direct change an action applies to one KPI.
type Effect struct {
	KPI    string  `yaml:"kpi" json:"kpi"`
	Change float64 `yaml:"change" json:"change"`
	// Mode is relative (default; Change is a fraction) or absolute (Change
	// is in the KPI's unit, so a KPI at zero can still move).
	Mode  EffectMode `yaml:"mode,omitempty" json:"mode,omitempty"`
	Delay Duration   `yaml:"delay,omitempty" json:"delay,omitempty"`
	// Saturation caps the achievable change (same unit as Change); larger
	// requested changes approach it with diminishing returns.
	Saturation float64 `yaml:"saturation,omitempty" json:"saturation,omitempty"`
	// Uncertainty is the +/- fraction of Change.
	Uncertainty float64 `yaml:"uncertainty,omitempty" json:"uncertainty,omitempty"`
}

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// Execute names an executor template and its parameters. Actions without it
// can be approved but have nothing to run.
type Execute struct {
	Template string            `yaml:"template" json:"template"`
	Params   map[string]string `yaml:"params,omitempty" json:"params,omitempty"`
}

type Action struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Adapter     string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	Risk        Risk     `yaml:"risk,omitempty" json:"risk,omitempty"`
	Effects     []Effect `yaml:"effects" json:"effects"`
	Execute     *Execute `yaml:"execute,omitempty" json:"execute,omitempty"`
	// Rollback is the compensating action proposed when the outcome regresses.
	Rollback *Execute      `yaml:"rollback,omitempty" json:"rollback,omitempty"`
	Outcome  *Outcome      `yaml:"outcome,omitempty" json:"outcome,omitempty"`
	Policy   *ActionPolicy `yaml:"policy,omitempty" json:"policy,omitempty"`
}

type Model struct {
	Name        string       `yaml:"name" json:"name"`
	KPIs        []KPI        `yaml:"kpis" json:"kpis"`
	Edges       []Edge       `yaml:"edges" json:"edges"`
	Actions     []Action     `yaml:"actions" json:"actions"`
	Constraints []Constraint `yaml:"constraints,omitempty" json:"constraints,omitempty"`

	index map[string]int
}

// AllConstraints returns the declared constraints plus one implicit
// constraint per critical KPI with a target: a critical target that is met
// must stay met, and one that is missed must not get worse.
func (m *Model) AllConstraints() []Constraint {
	out := append([]Constraint(nil), m.Constraints...)
	for _, k := range m.KPIs {
		if k.Criticality != CriticalityCritical || k.Target == nil {
			continue
		}
		c := Constraint{KPI: k.ID, Implicit: true, Why: "critical target"}
		t := *k.Target
		switch {
		case !k.Meets(k.Value):
			c.MustNotWorsen = true
		case k.Direction == HigherIsBetter:
			c.Floor = &t
		case k.Direction == LowerIsBetter:
			c.Ceiling = &t
		}
		out = append(out, c)
	}
	return out
}

// Violation describes a constraint breached by a value.
type Violation struct {
	KPI      string  `json:"kpi"`
	Value    float64 `json:"value"`
	Limit    float64 `json:"limit"`
	Kind     string  `json:"kind"` // floor | ceiling | must-not-worsen
	Implicit bool    `json:"implicit,omitempty"`
	Text     string  `json:"text"`
}

// Check reports whether moving kpi k from before to after breaches c. A
// floor or ceiling that is already breached only counts if the move takes
// the value further past it, so an existing breach never blocks a fix.
func (c Constraint) Check(k KPI, before, after float64) (Violation, bool) {
	v := Violation{KPI: c.KPI, Value: after, Implicit: c.Implicit}
	why := ""
	if c.Why != "" {
		why = " (" + c.Why + ")"
	}
	switch {
	case c.Floor != nil && after < *c.Floor && !(before < *c.Floor && after >= before):
		v.Kind, v.Limit = "floor", *c.Floor
		v.Text = fmt.Sprintf("%s would be %.4g, below the floor of %.4g%s", c.KPI, after, *c.Floor, why)
		return v, true
	case c.Ceiling != nil && after > *c.Ceiling && !(before > *c.Ceiling && after <= before):
		v.Kind, v.Limit = "ceiling", *c.Ceiling
		v.Text = fmt.Sprintf("%s would be %.4g, above the ceiling of %.4g%s", c.KPI, after, *c.Ceiling, why)
		return v, true
	case c.MustNotWorsen && k.Worse(before, after) && !near(before, after):
		v.Kind, v.Limit = "must-not-worsen", before
		v.Text = fmt.Sprintf("%s would worsen from %.4g to %.4g%s", c.KPI, before, after, why)
		return v, true
	}
	return Violation{}, false
}

func near(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	s := a
	if s < 0 {
		s = -s
	}
	return d <= 1e-9*(1+s)
}

// Version is a short content hash of the model structure: KPIs, targets,
// edges, actions and constraints, excluding live KPI values. It changes when
// the model is edited, not when signals refresh.
func (m *Model) Version() string {
	c := m.Clone()
	for i := range c.KPIs {
		c.KPIs[i].Value = 0
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "unknown"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Load reads and validates a model file.
func Load(path string) (*Model, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Model, error) {
	var m Model
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse model: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks references, directions and that edges form a DAG.
func (m *Model) Validate() error {
	m.index = make(map[string]int, len(m.KPIs))
	for i := range m.KPIs {
		k := &m.KPIs[i]
		if k.ID == "" {
			return fmt.Errorf("kpi #%d: missing id", i)
		}
		if _, dup := m.index[k.ID]; dup {
			return fmt.Errorf("kpi %q: duplicate id", k.ID)
		}
		if k.Name == "" {
			k.Name = k.ID
		}
		switch k.Direction {
		case HigherIsBetter, LowerIsBetter:
		case "":
			if k.Target != nil {
				return fmt.Errorf("kpi %q: target set without direction (higher|lower)", k.ID)
			}
		default:
			return fmt.Errorf("kpi %q: direction must be higher or lower, got %q", k.ID, k.Direction)
		}
		if _, ok := criticalityWeight[k.Criticality]; !ok {
			return fmt.Errorf("kpi %q: criticality must be critical, high, normal or low, got %q", k.ID, k.Criticality)
		}
		if k.Min != nil && k.Max != nil && *k.Min > *k.Max {
			return fmt.Errorf("kpi %q: min %v is above max %v", k.ID, *k.Min, *k.Max)
		}
		m.index[k.ID] = i
	}
	for _, c := range m.Constraints {
		k, ok := m.index[c.KPI]
		if !ok {
			return fmt.Errorf("constraint: unknown kpi %q", c.KPI)
		}
		if c.Floor == nil && c.Ceiling == nil && !c.MustNotWorsen {
			return fmt.Errorf("constraint on %q: set floor, ceiling or mustNotWorsen", c.KPI)
		}
		if c.Floor != nil && c.Ceiling != nil && *c.Floor > *c.Ceiling {
			return fmt.Errorf("constraint on %q: floor is above ceiling", c.KPI)
		}
		if c.MustNotWorsen && m.KPIs[k].Direction == "" {
			return fmt.Errorf("constraint on %q: mustNotWorsen needs the kpi to have a direction", c.KPI)
		}
	}
	for _, e := range m.Edges {
		if e.Confidence < 0 {
			return fmt.Errorf("edge %s->%s: confidence must not be negative", e.From, e.To)
		}
		switch e.Provenance {
		case "", ProvenanceDeclared, ProvenanceLearned:
		default:
			return fmt.Errorf("edge %s->%s: provenance must be declared or learned", e.From, e.To)
		}
		if _, ok := m.index[e.From]; !ok {
			return fmt.Errorf("edge %s->%s: unknown kpi %q", e.From, e.To, e.From)
		}
		if _, ok := m.index[e.To]; !ok {
			return fmt.Errorf("edge %s->%s: unknown kpi %q", e.From, e.To, e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("edge %s->%s: self loop", e.From, e.To)
		}
	}
	seen := map[string]bool{}
	for _, a := range m.Actions {
		if a.ID == "" {
			return fmt.Errorf("action %q: missing id", a.Name)
		}
		if seen[a.ID] {
			return fmt.Errorf("action %q: duplicate id", a.ID)
		}
		seen[a.ID] = true
		switch a.Risk {
		case "", RiskLow, RiskMedium, RiskHigh:
		default:
			return fmt.Errorf("action %q: risk must be low, medium or high", a.ID)
		}
		if len(a.Effects) == 0 {
			return fmt.Errorf("action %q: no effects", a.ID)
		}
		for _, ef := range a.Effects {
			if _, ok := m.index[ef.KPI]; !ok {
				return fmt.Errorf("action %q: unknown kpi %q", a.ID, ef.KPI)
			}
			switch ef.Mode {
			case "", EffectRelative, EffectAbsolute:
			default:
				return fmt.Errorf("action %q: effect mode must be relative or absolute", a.ID)
			}
			if ef.Saturation < 0 || ef.Uncertainty < 0 {
				return fmt.Errorf("action %q: saturation and uncertainty must not be negative", a.ID)
			}
		}
		if a.Rollback != nil && a.Rollback.Template == "" {
			return fmt.Errorf("action %q: rollback needs a template", a.ID)
		}
		if o := a.Outcome; o != nil {
			if o.Samples < 0 || o.Tolerance < 0 {
				return fmt.Errorf("action %q: outcome samples and tolerance must not be negative", a.ID)
			}
			for _, c := range o.Success {
				if _, ok := m.index[c.KPI]; !ok {
					return fmt.Errorf("action %q: success criterion on unknown kpi %q", a.ID, c.KPI)
				}
				if !criterionOps[c.Op] {
					return fmt.Errorf("action %q: criterion op must be <, <=, >, >= or met, got %q", a.ID, c.Op)
				}
				if c.Op != "met" && c.Value == nil {
					return fmt.Errorf("action %q: criterion on %q needs a value", a.ID, c.KPI)
				}
			}
			for _, g := range o.Guardrails {
				if _, ok := m.index[g]; !ok {
					return fmt.Errorf("action %q: unknown guardrail kpi %q", a.ID, g)
				}
			}
		}
		if p := a.Policy; p != nil {
			if p.Approvals < 0 {
				return fmt.Errorf("action %q: approvals must not be negative", a.ID)
			}
			if !keepModes[p.Keep] {
				return fmt.Errorf("action %q: keep must be required, preferred or off", a.ID)
			}
		}
	}
	if _, err := m.TopoOrder(); err != nil {
		return err
	}
	return nil
}

func (m *Model) KPI(id string) (*KPI, bool) {
	if m.index == nil {
		if err := m.Validate(); err != nil {
			return nil, false
		}
	}
	i, ok := m.index[id]
	if !ok {
		return nil, false
	}
	return &m.KPIs[i], true
}

func (m *Model) Action(id string) (*Action, bool) {
	for i := range m.Actions {
		if m.Actions[i].ID == id {
			return &m.Actions[i], true
		}
	}
	return nil, false
}

// TopoOrder returns KPI ids so that every edge goes from an earlier to a later
// id. Ties are broken by model order so results are deterministic.
func (m *Model) TopoOrder() ([]string, error) {
	pos := make(map[string]int, len(m.KPIs))
	for i, k := range m.KPIs {
		pos[k.ID] = i
	}
	indeg := make(map[string]int, len(m.KPIs))
	out := make(map[string][]string)
	for _, e := range m.Edges {
		indeg[e.To]++
		out[e.From] = append(out[e.From], e.To)
	}
	var ready []string
	for _, k := range m.KPIs {
		if indeg[k.ID] == 0 {
			ready = append(ready, k.ID)
		}
	}
	order := make([]string, 0, len(m.KPIs))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return pos[ready[i]] < pos[ready[j]] })
		n := ready[0]
		ready = ready[1:]
		order = append(order, n)
		for _, to := range out[n] {
			indeg[to]--
			if indeg[to] == 0 {
				ready = append(ready, to)
			}
		}
	}
	if len(order) != len(m.KPIs) {
		return nil, fmt.Errorf("edges contain a cycle; the KPI graph must be acyclic")
	}
	return order, nil
}

func cloneF(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneExec(e *Execute) *Execute {
	if e == nil {
		return nil
	}
	ex := *e
	if ex.Params != nil {
		p := make(map[string]string, len(ex.Params))
		for pk, pv := range ex.Params {
			p[pk] = pv
		}
		ex.Params = p
	}
	return &ex
}

// Clone returns a deep copy so simulations never mutate the live model.
func (m *Model) Clone() *Model {
	c := &Model{Name: m.Name}
	c.KPIs = make([]KPI, len(m.KPIs))
	for i, k := range m.KPIs {
		k.Target, k.Min, k.Max = cloneF(k.Target), cloneF(k.Min), cloneF(k.Max)
		if k.Freshness != nil {
			f := *k.Freshness
			k.Freshness = &f
		}
		if k.Source != nil {
			s := *k.Source
			if s.Labels != nil {
				l := make(map[string]string, len(s.Labels))
				for lk, lv := range s.Labels {
					l[lk] = lv
				}
				s.Labels = l
			}
			k.Source = &s
		}
		c.KPIs[i] = k
	}
	c.Edges = append([]Edge(nil), m.Edges...)
	c.Actions = make([]Action, len(m.Actions))
	for i, a := range m.Actions {
		a.Effects = append([]Effect(nil), a.Effects...)
		a.Execute, a.Rollback = cloneExec(a.Execute), cloneExec(a.Rollback)
		if a.Outcome != nil {
			o := *a.Outcome
			o.Success = make([]Criterion, len(a.Outcome.Success))
			for j, cr := range a.Outcome.Success {
				cr.Value = cloneF(cr.Value)
				o.Success[j] = cr
			}
			o.Guardrails = append([]string(nil), a.Outcome.Guardrails...)
			a.Outcome = &o
		}
		if a.Policy != nil {
			p := *a.Policy
			p.Windows = append([]string(nil), a.Policy.Windows...)
			if a.Policy.RequireFresh != nil {
				b := *a.Policy.RequireFresh
				p.RequireFresh = &b
			}
			a.Policy = &p
		}
		c.Actions[i] = a
	}
	if m.Constraints != nil {
		c.Constraints = make([]Constraint, len(m.Constraints))
		for i, k := range m.Constraints {
			k.Floor, k.Ceiling = cloneF(k.Floor), cloneF(k.Ceiling)
			c.Constraints[i] = k
		}
	}
	_ = c.Validate()
	return c
}
