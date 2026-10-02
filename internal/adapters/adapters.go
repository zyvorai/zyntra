// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package adapters refreshes KPI values from live sources. All adapters are
// read-only.
package adapters

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters/httpsrc"
	"github.com/zyvorai/zyntra/internal/adapters/kubernetes"
	"github.com/zyvorai/zyntra/internal/adapters/prometheus"
	"github.com/zyvorai/zyntra/internal/graph"
)

type Config struct {
	Prometheus *prometheus.Client
	Kubernetes kubernetes.Runner
	// Endpoints are named HTTP sources (netra, gravia, fabric, ...).
	Endpoints map[string]*httpsrc.Client
	// Rates keeps counter samples between refreshes for rate sources.
	Rates *RateTracker
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
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	OK        bool      `json:"ok"`
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
	Warming bool   `json:"warming,omitempty"`
	Error   string `json:"error,omitempty"`
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
		rep.KPIs[id] = KPIUpdate{Error: err.Error()}
	}
	for i := range m.KPIs {
		k := &m.KPIs[i]
		if !k.Live() {
			continue
		}
		v, served, err := r.value(ctx, *k.Source)
		if !served {
			rep.KPIs[k.ID] = KPIUpdate{Error: "source " + sourceName(*k.Source) + " is not configured"}
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
		k.Value = v
		rep.KPIs[k.ID] = KPIUpdate{OK: true}
	}
	for i := range m.KPIs {
		k := m.KPIs[i]
		if k.Source == nil {
			continue
		}
		if st, ok := r.status[sourceName(*k.Source)]; ok {
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

func (r *run) record(name, kind string, start time.Time, err error) {
	st, ok := r.status[name]
	if !ok {
		st = &Status{Name: name, Kind: kind, OK: true}
		r.status[name] = st
	}
	st.CheckedAt = time.Now()
	if d := time.Since(start).Milliseconds(); d > st.LatencyMS {
		st.LatencyMS = d
	}
	if err != nil && st.OK {
		st.OK = false
		st.Error = err.Error()
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
