// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package adapters refreshes KPI values from live sources. All adapters are
// read-only.
package adapters

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters/httpsrc"
	"github.com/zyvorai/zyntra/internal/adapters/kubernetes"
	"github.com/zyvorai/zyntra/internal/adapters/prometheus"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/inputs"
)

type Config struct {
	Prometheus *prometheus.Client
	Kubernetes kubernetes.Runner
	// Endpoints are named HTTP sources (netra, gravia, fabric, ...).
	Endpoints map[string]*httpsrc.Client
	// Rates keeps counter samples between refreshes for rate sources.
	Rates *RateTracker
	// Files caches parsed file sources between refreshes.
	Files *FileCache
	// Inputs holds webhook-in documents and manual values.
	Inputs *inputs.Store
	// HTTP is the client for http and sheet sources.
	HTTP *http.Client
	// Holds keeps the last in-window value of KPIs with a calendar while
	// outside the window. Nil disables holding (one-shot CLI runs).
	Holds *HoldTracker
	// Now is the clock (tests); nil means time.Now.
	Now func() time.Time
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Generic reports whether m has KPIs read by generic source kinds.
func Generic(m *graph.Model) bool {
	for _, k := range m.KPIs {
		if k.Source != nil && k.Source.Generic() {
			return true
		}
	}
	return false
}

// RateTracker remembers the last raw counter value per KPI.
type RateTracker struct {
	mu   sync.Mutex
	last map[string]counterSample
	now  func() time.Time
}

type counterSample struct {
	v float64
	t time.Time
}

func NewRateTracker() *RateTracker {
	return &RateTracker{last: map[string]counterSample{}, now: time.Now}
}

// Observe records raw and returns the per-second rate since the previous
// observation. ok is false on the first sample. A counter reset counts from 0.
func (r *RateTracker) Observe(id string, raw float64) (rate float64, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	prev, seen := r.last[id]
	r.last[id] = counterSample{raw, now}
	if !seen {
		return 0, false
	}
	dt := now.Sub(prev.t).Seconds()
	if dt <= 0 {
		return 0, false
	}
	delta := raw - prev.v
	if delta < 0 {
		delta = raw
	}
	return delta / dt, true
}

// Status is the health of one source after a refresh.
type Status struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	OK   bool   `json:"ok"`
	// State is ok, stale, error or fallback (not configured; KPIs keep
	// their last or model value).
	State     string    `json:"state"`
	Error     string    `json:"error,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
	KPIs      []string  `json:"kpis"`
	CheckedAt time.Time `json:"checked_at"`
}

// KPIUpdate is what happened to one live KPI during a refresh.
type KPIUpdate struct {
	// OK means the value was updated from its source.
	OK bool `json:"ok"`
	// Warming means the source answered but a rate needs a second sample.
	Warming bool `json:"warming,omitempty"`
	// Held means the KPI is outside its calendar window and keeps its last
	// in-window value.
	Held bool `json:"held,omitempty"`
	// Fallback means the value was not updated and still shows the last
	// good or model value.
	Fallback bool   `json:"fallback,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Report is the outcome of one refresh.
type Report struct {
	Sources []Status             `json:"sources"`
	KPIs    map[string]KPIUpdate `json:"kpis"`
}

// Refresh updates every KPI that has a source the config can serve. KPIs with
// unavailable sources keep their previous value; Report.KPIs says which ones
// were actually updated so callers can track freshness. Sources are sorted
// by name; the error joins per-KPI failures.
func Refresh(ctx context.Context, m *graph.Model, cfg Config) (Report, error) {
	r := &run{cfg: cfg, status: map[string]*Status{}, scrapes: map[string]scrape{}, docs: map[string]doc{}}
	rep := Report{KPIs: map[string]KPIUpdate{}}
	var errs []error
	fail := func(id string, err error) {
		errs = append(errs, fmt.Errorf("kpi %s: %w", id, err))
		rep.KPIs[id] = KPIUpdate{Error: err.Error(), Fallback: true}
	}
	now := cfg.now()
	for i := range m.KPIs {
		k := &m.KPIs[i]
		if !k.Live() {
			continue
		}
		var (
			v      float64
			served bool
			err    error
		)
		if k.Source.Generic() {
			v, served, err = r.generic(ctx, m, k)
		} else {
			v, served, err = r.value(ctx, *k.Source)
		}
		if !served {
			name := sourceName(*k.Source)
			r.recordState(name, k.Source.Kind, StateFallback, "not configured")
			rep.KPIs[k.ID] = KPIUpdate{Error: "source " + name + " is not configured", Fallback: true}
			continue
		}
		if err != nil {
			fail(k.ID, err)
			continue
		}
		if k.Source.Rate {
			if cfg.Rates == nil {
				fail(k.ID, errors.New("rate source without a rate tracker"))
				continue
			}
			rate, ok := cfg.Rates.Observe(k.ID, v)
			if !ok {
				rep.KPIs[k.ID] = KPIUpdate{Warming: true}
				continue
			}
			v = rate
		}
		if k.Source.Scale != 0 {
			v *= k.Source.Scale
		}
		if win, ok := m.CalendarFor(*k); ok && cfg.Holds != nil {
			if !win.Contains(now) && cfg.Holds.has(k.ID) {
				rep.KPIs[k.ID] = KPIUpdate{Held: true}
				continue
			}
			if win.Contains(now) {
				cfg.Holds.mark(k.ID)
			}
		}
		k.Value = v
		rep.KPIs[k.ID] = KPIUpdate{OK: true}
	}
	for i := range m.KPIs {
		k := m.KPIs[i]
		if k.Source == nil {
			continue
		}
		if st, ok := r.status[statusName(*k.Source)]; ok {
			st.KPIs = append(st.KPIs, k.ID)
		}
	}
	out := make([]Status, 0, len(r.status))
	for _, s := range r.status {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	rep.Sources = out
	return rep, errors.Join(errs...)
}

type scrape struct {
	samples []httpsrc.Sample
	err     error
}

type doc struct {
	v   any
	err error
}

type run struct {
	cfg     Config
	status  map[string]*Status
	scrapes map[string]scrape
	docs    map[string]doc
	inv     kubernetes.Inventory
	invErr  error
	invDone bool
}

func sourceName(s graph.Source) string {
	switch s.Kind {
	case "prometheus", "kubernetes":
		return s.Kind
	case "gravia", "netra", "fabric":
		if s.Endpoint == "" {
			return s.Kind
		}
	}
	return s.Endpoint
}

// statusName is the Status.Name a KPI's source reports under.
func statusName(s graph.Source) string {
	switch s.Kind {
	case graph.SourceFile:
		return "file:" + s.File
	case graph.SourceHTTP, graph.SourceSheet:
		return s.URL
	case graph.SourceWebhookIn:
		return "webhook:" + s.Name
	case graph.SourceManual:
		return "manual"
	}
	return sourceName(s)
}

var stateRank = map[string]int{StateOK: 0, StateStale: 1, StateFallback: 2, StateError: 3}

func (r *run) status1(name, kind string) *Status {
	st, ok := r.status[name]
	if !ok {
		st = &Status{Name: name, Kind: kind, OK: true, State: StateOK}
		r.status[name] = st
	}
	st.CheckedAt = time.Now()
	return st
}

func (r *run) record(name, kind string, start time.Time, err error) {
	st := r.status1(name, kind)
	if d := time.Since(start).Milliseconds(); d > st.LatencyMS {
		st.LatencyMS = d
	}
	if err != nil && st.OK {
		st.OK = false
		st.Error = err.Error()
		st.State = StateError
	}
}

// recordState sets a non-request health state; the worst state wins.
func (r *run) recordState(name, kind, state, msg string) {
	st := r.status1(name, kind)
	if stateRank[state] < stateRank[st.State] {
		return
	}
	st.State = state
	if state != StateOK {
		st.OK = false
		if msg != "" {
			st.Error = msg
		}
	}
}

// value returns the KPI value; served is false when the config has no
// client for the source.
func (r *run) value(ctx context.Context, s graph.Source) (float64, bool, error) {
	switch s.Kind {
	case "prometheus":
		if r.cfg.Prometheus == nil {
			return 0, false, nil
		}
		start := time.Now()
		v, err := r.cfg.Prometheus.Query(ctx, s.Query)
		r.record("prometheus", "prometheus", start, err)
		return v, true, err
	case "kubernetes":
		if r.cfg.Kubernetes == nil {
			return 0, false, nil
		}
		if !r.invDone {
			start := time.Now()
			r.inv, r.invErr = kubernetes.Collect(ctx, r.cfg.Kubernetes)
			r.invDone = true
			r.record("kubernetes", "kubernetes", start, r.invErr)
		}
		if r.invErr != nil {
			return 0, true, r.invErr
		}
		v, ok := r.inv[s.Metric]
		if !ok {
			return 0, true, fmt.Errorf("kubernetes metric %q not available", s.Metric)
		}
		return v, true, nil
	case "metrics":
		c := r.cfg.Endpoints[s.Endpoint]
		if c == nil {
			return 0, false, nil
		}
		path := s.Path
		if path == "" {
			path = "/metrics"
		}
		key := s.Endpoint + path
		sc, ok := r.scrapes[key]
		if !ok {
			start := time.Now()
			b, err := c.Get(ctx, path)
			if err == nil {
				sc.samples = httpsrc.ParseMetrics(b)
			}
			sc.err = err
			r.scrapes[key] = sc
			r.record(s.Endpoint, "metrics", start, err)
		}
		if sc.err != nil {
			return 0, true, sc.err
		}
		v, err := httpsrc.Aggregate(sc.samples, s.Metric, s.Labels, s.Agg)
		return v, true, err
	case "json", "gravia", "netra", "fabric":
		name := sourceName(s)
		c := r.cfg.Endpoints[name]
		if c == nil {
			return 0, false, nil
		}
		key := name + s.Path
		d, ok := r.docs[key]
		if !ok {
			start := time.Now()
			d.v, d.err = c.GetJSON(ctx, s.Path)
			r.docs[key] = d
			r.record(name, "json", start, d.err)
		}
		if d.err != nil {
			return 0, true, d.err
		}
		v, err := httpsrc.Field(d.v, s.Field)
		return v, true, err
	}
	return 0, true, fmt.Errorf("unknown source kind %q", s.Kind)
}
