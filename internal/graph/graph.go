// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package graph holds the KPI dependency model: KPIs, weighted edges between
// them, and the candidate actions that can move them.
package graph

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Direction says which way a KPI should move to be healthier.
type Direction string

const (
	HigherIsBetter Direction = "higher"
	LowerIsBetter  Direction = "lower"
)

// Source binds a KPI to a live data source. Empty Kind means the value in the
// model file is used as-is.
type Source struct {
	Kind   string `yaml:"kind" json:"kind"`
	Query  string `yaml:"query,omitempty" json:"query,omitempty"`
	Metric string `yaml:"metric,omitempty" json:"metric,omitempty"`
}

type KPI struct {
	ID        string    `yaml:"id" json:"id"`
	Name      string    `yaml:"name" json:"name"`
	Unit      string    `yaml:"unit,omitempty" json:"unit,omitempty"`
	Owner     string    `yaml:"owner,omitempty" json:"owner,omitempty"`
	Value     float64   `yaml:"value" json:"value"`
	Target    *float64  `yaml:"target,omitempty" json:"target,omitempty"`
	Direction Direction `yaml:"direction,omitempty" json:"direction,omitempty"`
	Source    *Source   `yaml:"source,omitempty" json:"source,omitempty"`
}

// Edge means: a relative change of x in From causes a relative change of
// Weight*x in To.
type Edge struct {
	From   string  `yaml:"from" json:"from"`
	To     string  `yaml:"to" json:"to"`
	Weight float64 `yaml:"weight" json:"weight"`
	Why    string  `yaml:"why,omitempty" json:"why,omitempty"`
}

// Effect is a direct relative change an action applies to one KPI.
type Effect struct {
	KPI    string  `yaml:"kpi" json:"kpi"`
	Change float64 `yaml:"change" json:"change"`
}

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

type Action struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	Adapter     string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	Risk        Risk     `yaml:"risk,omitempty" json:"risk,omitempty"`
	Effects     []Effect `yaml:"effects" json:"effects"`
}

type Model struct {
	Name    string   `yaml:"name" json:"name"`
	KPIs    []KPI    `yaml:"kpis" json:"kpis"`
	Edges   []Edge   `yaml:"edges" json:"edges"`
	Actions []Action `yaml:"actions" json:"actions"`

	index map[string]int
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
		m.index[k.ID] = i
	}
	for _, e := range m.Edges {
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

// Clone returns a deep copy so simulations never mutate the live model.
func (m *Model) Clone() *Model {
	c := &Model{Name: m.Name}
	c.KPIs = make([]KPI, len(m.KPIs))
	for i, k := range m.KPIs {
		if k.Target != nil {
			t := *k.Target
			k.Target = &t
		}
		if k.Source != nil {
			s := *k.Source
			k.Source = &s
		}
		c.KPIs[i] = k
	}
	c.Edges = append([]Edge(nil), m.Edges...)
	c.Actions = make([]Action, len(m.Actions))
	for i, a := range m.Actions {
		a.Effects = append([]Effect(nil), a.Effects...)
		c.Actions[i] = a
	}
	_ = c.Validate()
	return c
}
