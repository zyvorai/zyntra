// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package scenario saves what-if plans: the actions, the assumptions, the
// model version and the data they ran on, and the result. Running one never
// changes the live model.
package scenario

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/sim"
)

// Scenario is one saved plan.
type Scenario struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Actions     []string           `json:"actions"`
	Assumptions map[string]float64 `json:"assumptions,omitempty"` // KPI id -> assumed current value
	Notes       string             `json:"notes,omitempty"`
	CreatedBy   string             `json:"created_by"`
	CreatedAt   time.Time          `json:"created_at"`
	// Stamped when the scenario runs.
	ModelVersion string    `json:"model_version,omitempty"`
	DataVersion  string    `json:"data_version,omitempty"`
	RanAt        time.Time `json:"ran_at,omitempty"`
	Result       *Result   `json:"result,omitempty"`
}

// Result is what a run produced.
type Result struct {
	KPIs           []sim.KPIResult     `json:"kpis"`
	WeightedBefore float64             `json:"weighted_before"`
	WeightedAfter  float64             `json:"weighted_after"`
	Closes         []string            `json:"gaps_closed"`
	Opens          []string            `json:"gaps_opened"`
	Blocked        []string            `json:"blocked,omitempty"`
	AtRiskBefore   []ontology.Risk     `json:"at_risk_before"`
	AtRiskAfter    []ontology.Risk     `json:"at_risk_after"`
	ExposedBefore  []ontology.Exposure `json:"exposed_before"`
	ExposedAfter   []ontology.Exposure `json:"exposed_after"`
}

// Run simulates sc against m. rd may be nil when the pack has no ontology.
// Results are deterministic: the same model, assumptions and data give the
// same Result.
func Run(m *graph.Model, rd *ontology.Reader, data string, sc Scenario, now time.Time) (Scenario, error) {
	if len(sc.Actions) == 0 {
		return sc, errors.New("a scenario needs at least one action")
	}
	work := m.Clone()
	for id, v := range sc.Assumptions {
		k, ok := work.KPI(id)
		if !ok {
			return sc, fmt.Errorf("assumption on unknown KPI %q", id)
		}
		k.Value = v
	}
	var acts []graph.Action
	for _, id := range sc.Actions {
		a, ok := work.Action(id)
		if !ok {
			return sc, fmt.Errorf("unknown action %q", id)
		}
		acts = append(acts, *a)
	}
	res, err := sim.ApplyPlan(work, acts, sim.Options{})
	if err != nil {
		return sc, err
	}
	out := &Result{KPIs: res.KPIs, WeightedBefore: res.WeightedBefore, WeightedAfter: res.WeightedAfter,
		Closes: res.GapsClosed, Opens: res.GapsOpened}
	for _, v := range res.Violations {
		out.Blocked = append(out.Blocked, v.Text)
	}
	out.Blocked = append(out.Blocked, res.PreconditionFailures...)
	if rd != nil {
		before := func(k string) bool {
			kp, ok := work.KPI(k)
			return ok && gaps.Severity(*kp, kp.Value) > 0
		}
		after := map[string]bool{}
		for _, k := range res.KPIs {
			if k.HasTarget && !k.MetAfter {
				after[k.KPI] = true
			}
		}
		out.AtRiskBefore = ontology.AtRisk(*rd, before)
		out.AtRiskAfter = ontology.AtRisk(*rd, func(k string) bool { return after[k] })
		out.ExposedBefore = ontology.Exposed(*rd, out.AtRiskBefore)
		out.ExposedAfter = ontology.Exposed(*rd, out.AtRiskAfter)
	}
	sc.Result, sc.ModelVersion, sc.DataVersion, sc.RanAt = out, m.Version(), data, now
	return sc, nil
}

// Store keeps scenarios in one JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	list map[string]Scenario
}

// Open loads the store; an empty path keeps it in memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, list: map[string]Scenario{}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(b, &s.list)
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(s.path+".tmp", b, 0o640); err != nil {
		return err
	}
	return os.Rename(s.path+".tmp", s.path)
}

// Put saves sc, assigning an id when it has none.
func (s *Store) Put(sc Scenario) (Scenario, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc.ID == "" {
		var b [6]byte
		_, _ = rand.Read(b[:])
		sc.ID = "scn-" + hex.EncodeToString(b[:])
	}
	s.list[sc.ID] = sc
	return sc, s.save()
}

// Get returns one scenario.
func (s *Store) Get(id string) (Scenario, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.list[id]
	return sc, ok
}

// List returns scenarios newest first.
func (s *Store) List() []Scenario {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Scenario, 0, len(s.list))
	for _, sc := range s.list {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Delete removes a scenario.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.list[id]; !ok {
		return errors.New("no such scenario")
	}
	delete(s.list, id)
	return s.save()
}

// Row is one KPI across the compared scenarios.
type Row struct {
	KPI    string    `json:"kpi"`
	Name   string    `json:"name"`
	Unit   string    `json:"unit,omitempty"`
	Before float64   `json:"before"`
	After  []float64 `json:"after"` // one per scenario, in the order given
	Met    []bool    `json:"met"`
}

// Comparison lines scenarios up KPI by KPI.
type Comparison struct {
	Scenarios []string  `json:"scenarios"`
	Rows      []Row     `json:"rows"`
	Weighted  []float64 `json:"weighted_after"`
	AtRisk    []int     `json:"objects_at_risk_after"`
	Exposed   []int     `json:"objects_exposed_after"`
}

// Compare needs every scenario to have been run.
func Compare(scs []Scenario) (Comparison, error) {
	c := Comparison{}
	idx := map[string]*Row{}
	for i, sc := range scs {
		if sc.Result == nil {
			return c, fmt.Errorf("scenario %s has not been run", sc.ID)
		}
		c.Scenarios = append(c.Scenarios, sc.ID)
		c.Weighted = append(c.Weighted, sc.Result.WeightedAfter)
		c.AtRisk = append(c.AtRisk, len(sc.Result.AtRiskAfter))
		c.Exposed = append(c.Exposed, len(sc.Result.ExposedAfter))
		for _, k := range sc.Result.KPIs {
			r, ok := idx[k.KPI]
			if !ok {
				r = &Row{KPI: k.KPI, Name: k.Name, Unit: k.Unit, Before: k.Before,
					After: make([]float64, len(scs)), Met: make([]bool, len(scs))}
				idx[k.KPI] = r
			}
			r.After[i], r.Met[i] = k.After, k.MetAfter
		}
	}
	for _, r := range idx {
		c.Rows = append(c.Rows, *r)
	}
	sort.Slice(c.Rows, func(i, j int) bool { return c.Rows[i].KPI < c.Rows[j].KPI })
	return c, nil
}
