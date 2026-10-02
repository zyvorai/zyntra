// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package review

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/graph"
)

// EdgeProposal is a dependency the history suggests and the model lacks.
// It is shown as "proposed, not in the model" until someone adds it.
type EdgeProposal struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Direction is "leads" when From's changes come one sample before To's,
	// or "together" when they move in the same sample and the direction is
	// a guess.
	Direction  string  `json:"direction"`
	R          float64 `json:"r"`
	N          int     `json:"n"`
	Weight     float64 `json:"weight"`
	WeightLow  float64 `json:"weight_low"`
	WeightHigh float64 `json:"weight_high"`
	Why        string  `json:"why"`
	Status     string  `json:"status"`
	YAML       string  `json:"yaml"`
}

// Edge-finder thresholds: enough paired changes and a strong correlation.
const (
	minPairs = 8
	minR     = 0.7
)

// changes aligns two series on shared timestamps and returns their
// relative step changes.
func changes(a, b []ai.Point) (da, db []float64) {
	bv := make(map[time.Time]float64, len(b))
	for _, p := range b {
		bv[p.T] = p.V
	}
	var xs, ys []float64
	for _, p := range a {
		if v, ok := bv[p.T]; ok {
			xs, ys = append(xs, p.V), append(ys, v)
		}
	}
	for i := 1; i < len(xs); i++ {
		da = append(da, rel(xs[i-1], xs[i]))
		db = append(db, rel(ys[i-1], ys[i]))
	}
	return da, db
}

func corr(x, y []float64) (r, slope, se float64, n int) {
	n = len(x)
	if n < 3 || n != len(y) {
		return 0, 0, 0, n
	}
	var mx, my float64
	for i := range x {
		mx, my = mx+x[i], my+y[i]
	}
	mx, my = mx/float64(n), my/float64(n)
	var sxy, sxx, syy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		sxy, sxx, syy = sxy+dx*dy, sxx+dx*dx, syy+dy*dy
	}
	if sxx == 0 || syy == 0 {
		return 0, 0, 0, n
	}
	r = sxy / math.Sqrt(sxx*syy)
	slope = sxy / sxx
	se = math.Sqrt(math.Max(0, (1-r*r)/float64(n-2))) * math.Sqrt(syy/sxx)
	return r, slope, se, n
}

func active(d []float64) int {
	c := 0
	for _, v := range d {
		if math.Abs(v) > 1e-9 {
			c++
		}
	}
	return c
}

// MissingEdges looks for KPI pairs whose changes correlate strongly in the
// history while no path joins them in the model. The weight band comes from
// the observed ratio of changes; it is a proposal, not a fact.
func MissingEdges(m *graph.Model, hist map[string][]ai.Point) []EdgeProposal {
	linked := edgeIndex(m)
	ids := make([]string, 0, len(m.KPIs))
	for _, k := range m.KPIs {
		if len(hist[k.ID]) > minPairs {
			ids = append(ids, k.ID)
		}
	}
	sort.Strings(ids)
	var out []EdgeProposal
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			if linked(a, b) {
				continue
			}
			da, db := changes(hist[a], hist[b])
			if active(da) < minPairs || active(db) < minPairs {
				continue
			}
			r0, s0, se0, n0 := corr(da, db)
			rab, sab, seab, nab := corr(da[:len(da)-1], db[1:])
			rba, sba, seba, nba := corr(db[:len(db)-1], da[1:])
			p := EdgeProposal{From: a, To: b, Direction: "together", R: r0, N: n0, Weight: s0, Status: "proposed, not in the model"}
			se := se0
			switch {
			case math.Abs(rab) >= minR && math.Abs(rab) > math.Abs(r0) && math.Abs(rab) >= math.Abs(rba):
				p.Direction, p.R, p.N, p.Weight, se = "leads", rab, nab, sab, seab
			case math.Abs(rba) >= minR && math.Abs(rba) > math.Abs(r0):
				p = EdgeProposal{From: b, To: a, Direction: "leads", R: rba, N: nba, Weight: sba, Status: p.Status}
				se = seba
			}
			if math.Abs(p.R) < minR || p.N < minPairs {
				continue
			}
			p.WeightLow, p.WeightHigh = round3s(p.Weight-1.96*se), round3s(p.Weight+1.96*se)
			p.R, p.Weight = round3s(p.R), round3s(p.Weight)
			lead := "in the same sample"
			if p.Direction == "leads" {
				lead = "one sample later"
			}
			p.Why = fmt.Sprintf("In %d paired changes %s and %s moved together %s (r=%.2f): a 10%% move in %s came with %+.1f%% in %s (band %+.2f to %+.2f).",
				p.N, p.From, p.To, lead, p.R, p.From, p.Weight*10, p.To, p.WeightLow, p.WeightHigh)
			if p.Direction == "together" {
				p.Why += " Which one drives the other is not known; check before saving."
			}
			conf := math.Min(0.6, math.Max(0.1, 1-math.Abs(p.WeightHigh-p.WeightLow)/math.Max(1e-9, 2*math.Abs(p.Weight))))
			p.YAML = fmt.Sprintf("- {from: %s, to: %s, weight: %.3g, confidence: %.2f, provenance: learned, why: %q}", p.From, p.To, p.Weight, conf, "observed: r="+fmt.Sprintf("%.2f", p.R)+" over "+fmt.Sprint(p.N)+" changes")
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return math.Abs(out[i].R) > math.Abs(out[j].R) })
	return out
}

func round3s(v float64) float64 { return math.Round(v*1000) / 1000 }
