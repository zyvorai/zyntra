// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package freshness tracks when each live KPI last refreshed successfully,
// so decisions can say which inputs are stale or missing instead of silently
// reusing old values.
package freshness

import (
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

// Status values.
const (
	Fresh   = "fresh"
	Stale   = "stale"
	Missing = "missing" // live KPI that has never refreshed
	Static  = "static"  // value comes from the model file
	Held    = "held"    // outside its calendar window; last in-window value
)

// State is the freshness of one KPI.
type State struct {
	KPI         string     `json:"kpi"`
	Status      string     `json:"status"`
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	ErrorAt     *time.Time `json:"error_at,omitempty"`
	AgeSeconds  float64    `json:"age_seconds,omitempty"`
	MaxAge      string     `json:"max_age,omitempty"`
	Required    bool       `json:"required,omitempty"`
}

// Usable reports whether the value can be trusted right now.
func (s State) Usable() bool { return s.Status == Fresh || s.Status == Static || s.Status == Held }

type entry struct {
	ok      time.Time
	err     string
	errAt   time.Time
	warming bool
	held    bool
}

// Tracker records refresh outcomes per KPI. The zero value is not usable;
// call New.
type Tracker struct {
	mu            sync.Mutex
	e             map[string]*entry
	defaultMaxAge time.Duration
	now           func() time.Time
}

// New returns a tracker whose KPIs go stale after defaultMaxAge unless their
// model sets freshness.maxAge.
func New(defaultMaxAge time.Duration) *Tracker {
	return &Tracker{e: map[string]*entry{}, defaultMaxAge: defaultMaxAge, now: time.Now}
}

// SetClock replaces the clock (tests).
func (t *Tracker) SetClock(now func() time.Time) { t.now = now }

func (t *Tracker) get(id string) *entry {
	x, ok := t.e[id]
	if !ok {
		x = &entry{}
		t.e[id] = x
	}
	return x
}

// Success records a successful refresh of id at.
func (t *Tracker) Success(id string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	x := t.get(id)
	x.ok, x.err, x.warming, x.held = at, "", false, false
}

// Held records that id is outside its calendar window and keeps its last
// in-window value.
func (t *Tracker) Held(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.get(id).held = true
}

// Failure records a failed refresh of id.
func (t *Tracker) Failure(id, err string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	x := t.get(id)
	x.err, x.errAt = err, at
}

// Warming records that a rate source answered but needs another sample.
func (t *Tracker) Warming(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.get(id).warming = true
}

func (t *Tracker) maxAge(k graph.KPI) time.Duration {
	if k.Freshness != nil && k.Freshness.MaxAge > 0 {
		return k.Freshness.MaxAge.D()
	}
	return t.defaultMaxAge
}

// States returns the freshness of every KPI in m, in model order. A nil
// tracker reports every KPI as static (CLI use).
func (t *Tracker) States(m *graph.Model) []State {
	out := make([]State, 0, len(m.KPIs))
	if t == nil {
		for _, k := range m.KPIs {
			out = append(out, State{KPI: k.ID, Status: Static})
		}
		return out
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for _, k := range m.KPIs {
		s := State{KPI: k.ID, Status: Static}
		if k.Freshness != nil {
			s.Required = k.Freshness.Required
		}
		if !k.Live() {
			out = append(out, s)
			continue
		}
		max := t.maxAge(k)
		if max > 0 {
			s.MaxAge = max.String()
		}
		x := t.e[k.ID]
		switch {
		case x == nil || x.ok.IsZero():
			s.Status = Missing
		default:
			ok := x.ok
			s.LastSuccess = &ok
			s.AgeSeconds = now.Sub(ok).Seconds()
			s.Status = Fresh
			switch {
			case x.held:
				s.Status = Held
			case max > 0 && now.Sub(ok) > max:
				s.Status = Stale
			}
		}
		if x != nil && x.err != "" {
			at := x.errAt
			s.LastError, s.ErrorAt = x.err, &at
		}
		if x != nil && x.warming && s.Status == Missing {
			s.LastError = "rate source warming up (needs a second sample)"
		}
		out = append(out, s)
	}
	return out
}

// Index returns states keyed by KPI id.
func Index(states []State) map[string]State {
	out := make(map[string]State, len(states))
	for _, s := range states {
		out[s.KPI] = s
	}
	return out
}

// Unusable returns the ids of stale or missing KPIs, sorted.
func Unusable(states []State) []string {
	var out []string
	for _, s := range states {
		if !s.Usable() {
			out = append(out, s.KPI)
		}
	}
	sort.Strings(out)
	return out
}

// Set returns the unusable KPI ids as a set.
func Set(states []State) map[string]bool {
	out := map[string]bool{}
	for _, s := range states {
		if !s.Usable() {
			out[s.KPI] = true
		}
	}
	return out
}
