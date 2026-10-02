// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package ai holds Zyntra's insight engine: KPI history, anomaly detection,
// time-to-breach forecasts, and grounded natural-language answers. Every
// number comes from deterministic code; an optional LLM only rewrites prose.
package ai

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// History is a bounded per-KPI time series.
type History struct {
	mu   sync.RWMutex
	max  int
	data map[string][]Point
}

func NewHistory(max int) *History {
	if max <= 0 {
		max = 720
	}
	return &History{max: max, data: map[string][]Point{}}
}

// Record appends the current value of every KPI.
// Record appends the current value of every KPI except those in skip (held
// outside their calendar window), so forecasts only see in-window samples.
func (h *History) Record(m *graph.Model, t time.Time, skip ...map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, k := range m.KPIs {
		if len(skip) > 0 && skip[0][k.ID] {
			continue
		}
		s := append(h.data[k.ID], Point{t, k.Value})
		if len(s) > h.max {
			s = s[len(s)-h.max:]
		}
		h.data[k.ID] = s
	}
}

func (h *History) Series(id string) []Point {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]Point(nil), h.data[id]...)
}

// Snapshot returns a copy of all series, for persistence.
func (h *History) Snapshot() map[string][]Point {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string][]Point, len(h.data))
	for k, v := range h.data {
		out[k] = append([]Point(nil), v...)
	}
	return out
}

func (h *History) Restore(d map[string][]Point) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range d {
		if len(v) > h.max {
			v = v[len(v)-h.max:]
		}
		h.data[k] = append([]Point(nil), v...)
	}
}

// Anomaly is a KPI whose latest value is far from its recent baseline.
type Anomaly struct {
	KPI      string  `json:"kpi"`
	Name     string  `json:"name"`
	Value    float64 `json:"value"`
	Mean     float64 `json:"mean"`
	StdDev   float64 `json:"stddev"`
	Z        float64 `json:"z"`
	Samples  int     `json:"samples"`
	Severity string  `json:"severity"`
	Text     string  `json:"text"`
}

const (
	minBaseline = 8
	zThreshold  = 3.0
)

// Anomalies flags KPIs whose latest sample has |z| > 3 against the preceding
// samples. Flat baselines flag any change.
func Anomalies(m *graph.Model, h *History) []Anomaly {
	var out []Anomaly
	for _, k := range m.KPIs {
		s := h.Series(k.ID)
		if len(s) < minBaseline+1 {
			continue
		}
		base, last := s[:len(s)-1], s[len(s)-1].V
		mean, sd := meanStd(base)
		var z float64
		switch {
		case sd > 1e-12:
			z = (last - mean) / sd
		case math.Abs(last-mean) > 1e-9*math.Max(1, math.Abs(mean)):
			z = math.Copysign(math.Inf(1), last-mean)
		default:
			continue
		}
		if math.Abs(z) <= zThreshold {
			continue
		}
		sev := "warning"
		if math.Abs(z) > 2*zThreshold {
			sev = "critical"
		}
		dir := "above"
		if z < 0 {
			dir = "below"
		}
		zs := "a flat baseline"
		if !math.IsInf(z, 0) {
			zs = fmtFloat(math.Abs(z)) + " standard deviations"
		}
		out = append(out, Anomaly{
			KPI: k.ID, Name: k.Name, Value: last, Mean: mean, StdDev: sd, Z: clampInf(z),
			Samples: len(base), Severity: sev,
			Text: k.Name + " is " + fmtFloat(last) + unitSuffix(k.Unit) + ", " + zs + " " + dir + " its recent mean of " + fmtFloat(mean) + unitSuffix(k.Unit) + ".",
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return math.Abs(out[i].Z) > math.Abs(out[j].Z) })
	return out
}

func clampInf(z float64) float64 {
	if math.IsInf(z, 1) {
		return 1e9
	}
	if math.IsInf(z, -1) {
		return -1e9
	}
	return z
}

func meanStd(p []Point) (float64, float64) {
	var sum float64
	for _, x := range p {
		sum += x.V
	}
	mean := sum / float64(len(p))
	var ss float64
	for _, x := range p {
		ss += (x.V - mean) * (x.V - mean)
	}
	return mean, math.Sqrt(ss / float64(len(p)))
}

// Forecast is a linear trend projection against a KPI's target.
type Forecast struct {
	KPI        string   `json:"kpi"`
	Name       string   `json:"name"`
	Status     string   `json:"status"` // breach | at-risk | improving | stable
	SlopePerHr float64  `json:"slope_per_hour"`
	ETASeconds *float64 `json:"eta_seconds,omitempty"`
	Value      float64  `json:"value"`
	Target     float64  `json:"target"`
	Samples    int      `json:"samples"`
	Text       string   `json:"text"`
}

const (
	minTrend = 6
	// Trends over shorter windows are mostly scrape noise.
	minSpan = 10 * time.Minute
	// Only warn about breaches projected within this horizon.
	horizon = 7 * 24 * time.Hour
)

// Forecasts fits a least-squares line to each targeted KPI's history.
func Forecasts(m *graph.Model, h *History) []Forecast {
	var out []Forecast
	for _, k := range m.KPIs {
		if k.Target == nil {
			continue
		}
		s := h.Series(k.ID)
		if len(s) < minTrend || s[len(s)-1].T.Sub(s[0].T) < minSpan {
			continue
		}
		slope := slopePerSecond(s)
		v, t := k.Value, *k.Target
		f := Forecast{KPI: k.ID, Name: k.Name, SlopePerHr: slope * 3600, Value: v, Target: t, Samples: len(s)}
		breached := (k.Direction == graph.HigherIsBetter && v < t) || (k.Direction == graph.LowerIsBetter && v > t)
		worsening := (k.Direction == graph.HigherIsBetter && slope < 0) || (k.Direction == graph.LowerIsBetter && slope > 0)
		switch {
		case breached && !worsening && math.Abs(slope) > 1e-12:
			f.Status = "improving"
			eta := (t - v) / slope
			f.ETASeconds = &eta
			f.Text = k.Name + " is missing target but trending back; on this trend it recovers in " + fmtDur(eta) + "."
		case breached:
			f.Status = "breach"
			f.Text = k.Name + " is already missing its target (" + fmtFloat(v) + " vs " + fmtFloat(t) + ")."
		case worsening:
			eta := (t - v) / slope
			if eta > horizon.Seconds() {
				f.Status = "stable"
				f.Text = k.Name + " is drifting toward its target but no breach is projected within 7 days."
				break
			}
			f.Status = "at-risk"
			f.ETASeconds = &eta
			f.Text = k.Name + " misses target in about " + fmtDur(eta) + " on its current trend."
		default:
			f.Status = "stable"
			f.Text = k.Name + " is on target with no adverse trend."
		}
		out = append(out, f)
	}
	rank := map[string]int{"breach": 0, "at-risk": 1, "improving": 2, "stable": 3}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		ei, ej := math.Inf(1), math.Inf(1)
		if out[i].ETASeconds != nil {
			ei = *out[i].ETASeconds
		}
		if out[j].ETASeconds != nil {
			ej = *out[j].ETASeconds
		}
		return ei < ej
	})
	return out
}

func slopePerSecond(p []Point) float64 {
	t0 := p[0].T
	var sx, sy, sxx, sxy float64
	n := float64(len(p))
	for _, x := range p {
		dt := x.T.Sub(t0).Seconds()
		sx += dt
		sy += x.V
		sxx += dt * dt
		sxy += dt * x.V
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}
