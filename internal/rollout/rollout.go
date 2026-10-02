// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package rollout tracks staged delivery of an approved decision. Zyntra does
// not deploy anything: external deployment tooling does the work and reports
// per-site results here. Zyntra decides, from live KPIs, whether the next
// stage may start, and stops the rollout when a stage fails or a health gate
// is not met. Unknown or stale gate data fails closed.
package rollout

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

// Site states a deployment tool may report.
const (
	SiteStarted = "started"
	SiteHealthy = "healthy"
	SiteFailed  = "failed"
)

// Stage and rollout states.
const (
	StageWaiting = "waiting" // an earlier stage is not finished
	StageRunning = "running"
	StageHealthy = "healthy"
	StageBlocked = "blocked" // every site healthy but a health gate is not met
	StageFailed  = "failed"

	RolloutOpen     = "open"
	RolloutComplete = "complete"
	RolloutHalted   = "halted"
	RolloutAborted  = "aborted"
)

// SiteReport is what the deployment tool said about one site.
type SiteReport struct {
	State string    `json:"state"`
	Note  string    `json:"note,omitempty"`
	By    string    `json:"by"`
	At    time.Time `json:"at"`
}

// GateCheck is one health gate evaluated against a live KPI.
type GateCheck struct {
	KPI    string   `json:"kpi"`
	Value  *float64 `json:"value,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	OK     bool     `json:"ok"`
	Reason string   `json:"reason,omitempty"`
}

// Stage is one wave of the rollout.
type Stage struct {
	Name      string                `json:"name"`
	Sites     []string              `json:"sites"`
	Gates     []ontology.Gate       `json:"gates,omitempty"`
	State     string                `json:"state"`
	Reports   map[string]SiteReport `json:"reports,omitempty"`
	GateCheck []GateCheck           `json:"gate_check,omitempty"`
}

// Rollout is the delivery of one approved decision.
type Rollout struct {
	ID        string    `json:"id"` // the decision (proposal) id
	Action    string    `json:"action"`
	Tenant    string    `json:"tenant,omitempty"`
	Stages    []Stage   `json:"stages"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Current is the stage allowed to run now ("" when finished or halted).
	Current string `json:"current,omitempty"`
}

// GateValue reports a KPI's current value and whether it can be trusted.
type GateValue func(kpi string) (value float64, usable bool, known bool)

// Store keeps rollouts in one JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	all  map[string]*Rollout
	now  func() time.Time
}

// Open loads the store; an empty path keeps it in memory.
func Open(path string) (*Store, error) {
	s := &Store{path: path, all: map[string]*Rollout{}, now: time.Now}
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
	return s, json.Unmarshal(b, &s.all)
}

func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.MarshalIndent(s.all, "", "  ")
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

// Open creates the rollout for an approved decision. It is idempotent: a
// second call for the same id returns the existing one.
func (s *Store) Create(id, action, tenant string, plan *ontology.Rollout) (Rollout, bool, error) {
	if plan == nil || len(plan.Stages) == 0 {
		return Rollout{}, false, errors.New("no rollout stages")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.all[id]; ok {
		return clone(r), false, nil
	}
	now := s.now().UTC()
	r := &Rollout{ID: id, Action: action, Tenant: tenant, State: RolloutOpen, CreatedAt: now, UpdatedAt: now}
	for i, st := range plan.Stages {
		stage := Stage{Name: st.Name, Sites: append([]string(nil), st.Sites...), Gates: st.Gates, State: StageWaiting, Reports: map[string]SiteReport{}}
		if i == 0 {
			stage.State = StageRunning
		}
		r.Stages = append(r.Stages, stage)
	}
	r.Current = r.Stages[0].Name
	s.all[id] = r
	return clone(r), true, s.save()
}

// Get returns one rollout.
func (s *Store) Get(id string) (Rollout, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.all[id]
	if !ok {
		return Rollout{}, false
	}
	return clone(r), true
}

// List returns rollouts, newest first.
func (s *Store) List() []Rollout {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Rollout, 0, len(s.all))
	for _, r := range s.all {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Errors a caller can map to HTTP statuses.
var (
	ErrNotFound   = errors.New("no such rollout")
	ErrNotCurrent = errors.New("that stage may not run yet")
	ErrFinished   = errors.New("the rollout is finished")
	ErrBadReport  = errors.New("bad report")
)

// Report records a site result and re-evaluates the rollout. Only the current
// stage accepts reports, so tooling cannot jump ahead of a gate.
func (s *Store) Report(id, stage, site, state, note, by string, gate GateValue) (Rollout, error) {
	if state != SiteStarted && state != SiteHealthy && state != SiteFailed {
		return Rollout{}, fmt.Errorf("%w: state must be started, healthy or failed", ErrBadReport)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.all[id]
	if !ok {
		return Rollout{}, ErrNotFound
	}
	if r.State == RolloutComplete || r.State == RolloutAborted {
		return clone(r), ErrFinished
	}
	st := r.stage(stage)
	if st == nil {
		return clone(r), fmt.Errorf("%w: no stage %q", ErrBadReport, stage)
	}
	if st.Name != r.Current {
		return clone(r), ErrNotCurrent
	}
	known := false
	for _, x := range st.Sites {
		known = known || x == site
	}
	if !known {
		return clone(r), fmt.Errorf("%w: site %q is not in stage %q", ErrBadReport, site, stage)
	}
	if len(note) > 500 {
		note = note[:500]
	}
	now := s.now().UTC()
	st.Reports[site] = SiteReport{State: state, Note: note, By: by, At: now}
	r.evaluate(gate, now)
	return clone(r), s.save()
}

// Recheck re-evaluates the current stage's gates (for a blocked stage whose
// KPI has since recovered) without a new report.
func (s *Store) Recheck(id string, gate GateValue) (Rollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.all[id]
	if !ok {
		return Rollout{}, ErrNotFound
	}
	if r.State == RolloutComplete || r.State == RolloutAborted {
		return clone(r), ErrFinished
	}
	r.evaluate(gate, s.now().UTC())
	return clone(r), s.save()
}

// Abort stops a rollout for good.
func (s *Store) Abort(id, reason string) (Rollout, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.all[id]
	if !ok {
		return Rollout{}, ErrNotFound
	}
	if r.State == RolloutComplete || r.State == RolloutAborted {
		return clone(r), ErrFinished
	}
	r.State, r.Current, r.Reason, r.UpdatedAt = RolloutAborted, "", "aborted: "+reason, s.now().UTC()
	return clone(r), s.save()
}

func (r *Rollout) stage(name string) *Stage {
	for i := range r.Stages {
		if r.Stages[i].Name == name {
			return &r.Stages[i]
		}
	}
	return nil
}

// evaluate moves the rollout forward from the current stage's reports.
func (r *Rollout) evaluate(gate GateValue, now time.Time) {
	r.UpdatedAt = now
	for {
		st := r.stage(r.Current)
		if st == nil {
			return
		}
		failed, healthy := 0, 0
		for _, site := range st.Sites {
			switch st.Reports[site].State {
			case SiteFailed:
				failed++
			case SiteHealthy:
				healthy++
			}
		}
		switch {
		case failed > 0:
			st.State, st.GateCheck = StageFailed, nil
			r.State, r.Reason = RolloutHalted, fmt.Sprintf("stage %s: %d site(s) reported failure", st.Name, failed)
			return
		case healthy < len(st.Sites):
			st.State, st.GateCheck = StageRunning, nil
			r.State, r.Reason = RolloutOpen, ""
			return
		}
		// Every site is healthy: the gates decide whether the next stage may start.
		st.GateCheck = checkGates(st.Gates, gate)
		for _, g := range st.GateCheck {
			if !g.OK {
				st.State = StageBlocked
				r.State, r.Reason = RolloutHalted, fmt.Sprintf("stage %s: health gate on %s not met: %s", st.Name, g.KPI, g.Reason)
				return
			}
		}
		st.State = StageHealthy
		next := r.nextAfter(st.Name)
		if next == nil {
			r.State, r.Current, r.Reason = RolloutComplete, "", ""
			return
		}
		next.State = StageRunning
		r.State, r.Current, r.Reason = RolloutOpen, next.Name, ""
		return
	}
}

func (r *Rollout) nextAfter(name string) *Stage {
	for i := range r.Stages {
		if r.Stages[i].Name == name && i+1 < len(r.Stages) {
			return &r.Stages[i+1]
		}
	}
	return nil
}

func checkGates(gates []ontology.Gate, value GateValue) []GateCheck {
	out := make([]GateCheck, 0, len(gates))
	for _, g := range gates {
		c := GateCheck{KPI: g.KPI, Max: g.Max, Min: g.Min}
		v, usable, known := value(g.KPI)
		switch {
		case !known:
			c.Reason = "the KPI is not in the model"
		case !usable:
			c.Reason = "its value is stale or missing, so the gate cannot be trusted"
		default:
			c.Value = &v
			c.OK = true
			if g.Max != nil && v > *g.Max {
				c.OK, c.Reason = false, fmt.Sprintf("%.4g is above the limit %.4g", v, *g.Max)
			}
			if g.Min != nil && v < *g.Min {
				c.OK, c.Reason = false, fmt.Sprintf("%.4g is below the limit %.4g", v, *g.Min)
			}
		}
		out = append(out, c)
	}
	return out
}

func clone(r *Rollout) Rollout {
	b, _ := json.Marshal(r)
	var c Rollout
	_ = json.Unmarshal(b, &c)
	return c
}
