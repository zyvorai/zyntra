// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package calibrate

import (
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/outcome"
	"github.com/zyvorai/zyntra/internal/sim"
)

const model = `
name: t
kpis:
  - {id: queue, value: 20, target: 10, direction: lower}
  - {id: gpus, value: 10, target: 12, direction: higher}
  - {id: wait, value: 100, target: 60, direction: lower}
edges:
  - {from: queue, to: wait, weight: 0.5, confidence: 0.2}
  - {from: gpus, to: wait, weight: -0.4}
actions: []
`

func load(t *testing.T) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// decision builds a finished, applied proposal that predicted a change in
// "wait" of direct+edgeQueue+edgeGPUs (relative), where reality made the
// queue edge worth trueQueueScale times its predicted size.
func decision(i int, direct, queueRel, gpuRel, trueQueueScale, noise float64) approvals.Proposal {
	const base = 100.0
	predRel := direct + 0.5*queueRel + -0.4*gpuRel
	actualRel := direct + trueQueueScale*0.5*queueRel + -0.4*gpuRel + noise
	return approvals.Proposal{
		ID: string(rune('a' + i)), Status: approvals.Executed,
		Baseline:  map[string]float64{"wait": base},
		Predicted: approvals.Prediction{KPIs: map[string]float64{"wait": base * (1 + predRel)}},
		Actual:    map[string]float64{"wait": base * (1 + actualRel)},
		Outcome:   &outcome.Record{State: outcome.Verified},
		Simulation: &sim.Result{Trace: []sim.Step{
			{To: "wait", Delta: direct},
			{From: "queue", To: "wait", Weight: 0.5, Delta: 0.5 * queueRel},
			{From: "gpus", To: "wait", Weight: -0.4, Delta: -0.4 * gpuRel},
		}},
	}
}

func decisions(n int, trueScale, noise float64) []approvals.Proposal {
	rng := rand.New(rand.NewPCG(1, 2))
	var ps []approvals.Proposal
	for i := 0; i < n; i++ {
		ps = append(ps, decision(i, -0.05*rng.Float64(), -0.2-0.3*rng.Float64(), 0.1*rng.Float64(), trueScale, noise*(rng.Float64()-0.5)))
	}
	return ps
}

func TestRecoversAWrongWeight(t *testing.T) {
	// The queue edge is really twice as strong as declared.
	rep := Analyze(load(t), decisions(12, 2, 0.002), Options{})
	if rep.Decisions != 12 || len(rep.Suggestions) != 1 {
		t.Fatalf("%+v", rep)
	}
	s := rep.Suggestions[0]
	if s.KPI != "wait" || s.Improvement < 0.5 {
		t.Fatalf("suggestion = %+v", s)
	}
	var q *EdgeFit
	for i := range s.Edges {
		if s.Edges[i].From == "queue" {
			q = &s.Edges[i]
		}
	}
	if q == nil || q.Scale < 1.4 || q.Scale > 2.1 || math.Abs(q.Suggested-0.5*q.Scale) > 0.01 {
		t.Fatalf("queue edge fit = %+v", q)
	}
	if !strings.Contains(s.YAML, "from: queue, to: wait, weight:") || !strings.Contains(s.YAML, "confidence: 0.2") || !strings.Contains(s.YAML, "was 0.5") {
		t.Errorf("yaml = %q", s.YAML)
	}
	for _, e := range s.Edges {
		if e.From == "gpus" {
			t.Errorf("a correct edge was changed: %+v", e)
		}
	}
	if k := rep.KPIs[0]; k.KPI != "wait" || k.N != 12 || k.MeanAbsError <= 0 {
		t.Errorf("backtest = %+v", k)
	}
}

func TestCorrectModelGetsNoSuggestion(t *testing.T) {
	rep := Analyze(load(t), decisions(12, 1, 0.01), Options{})
	if len(rep.Suggestions) != 0 {
		t.Fatalf("a correct model was 'fixed': %+v", rep.Suggestions)
	}
	if len(rep.Notes) == 0 {
		t.Error("the report should say why nothing was suggested")
	}
}

func TestPureNoiseDoesNotOverfit(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	var ps []approvals.Proposal
	for i := 0; i < 20; i++ {
		ps = append(ps, decision(i, 0, -0.25, 0.05, 1, (rng.Float64()-0.5)*0.4))
	}
	if rep := Analyze(load(t), ps, Options{}); len(rep.Suggestions) != 0 {
		t.Fatalf("noise produced a suggestion: %+v", rep.Suggestions[0])
	}
}

func TestNeedsEnoughDecisions(t *testing.T) {
	rep := Analyze(load(t), decisions(3, 2, 0), Options{})
	if len(rep.Suggestions) != 0 || len(rep.Notes) == 0 || !strings.Contains(rep.Notes[0], "needed") {
		t.Fatalf("%+v", rep)
	}
}

func TestOnlyFinishedAppliedDecisionsCount(t *testing.T) {
	ps := decisions(8, 2, 0)
	ps[0].Status = approvals.Pending
	ps[1].Actual = nil // a dry-run: nothing was observed
	ps[2].Outcome = &outcome.Record{State: outcome.Observing}
	ps[3].Outcome = &outcome.Record{State: outcome.Inconclusive}
	ps[4].Simulation = nil
	rep := Analyze(load(t), ps, Options{})
	if rep.Decisions != 3 {
		t.Fatalf("counted %d decisions, want 3", rep.Decisions)
	}
	dry := Analyze(load(t), []approvals.Proposal{{Status: approvals.Executed}}, Options{})
	if dry.Decisions != 0 || !strings.Contains(dry.Note, "dry-run") {
		t.Fatalf("a dry-run report should explain itself: %+v", dry)
	}
}

func TestScalesAreBounded(t *testing.T) {
	// Reality says the edge is 50x stronger; one suggestion may not move it past the cap.
	rep := Analyze(load(t), decisions(12, 50, 0), Options{})
	for _, s := range rep.Suggestions {
		for _, e := range s.Edges {
			if e.Scale > 4.001 || e.Scale < 0.249 {
				t.Errorf("scale %v escaped its bounds", e.Scale)
			}
		}
	}
}

func TestSolveAndRidge(t *testing.T) {
	x := solve([][]float64{{2, 1}, {1, 3}}, []float64{5, 10})
	if math.Abs(x[0]-1) > 1e-9 || math.Abs(x[1]-3) > 1e-9 {
		t.Fatalf("solve = %v", x)
	}
	// An edge that never contributed stays at exactly 1.
	got := ridge([][]float64{{0.2, 0}, {0.3, 0}, {0.1, 0}}, []float64{0.4, 0.6, 0.2}, nil, 3)
	if math.Abs(got[1]-1) > 1e-6 {
		t.Errorf("a no-signal edge moved to %v", got[1])
	}
}

func seeded(seed uint64, n int, trueScale, noise float64) []approvals.Proposal {
	rng := rand.New(rand.NewPCG(seed, seed*7+1))
	var ps []approvals.Proposal
	for i := 0; i < n; i++ {
		ps = append(ps, decision(i, -0.05*rng.Float64(), -0.2-0.3*rng.Float64(), 0.1*rng.Float64(), trueScale, noise*(rng.Float64()-0.5)))
	}
	return ps
}

// Across many random datasets: a correct model is almost never "fixed", a
// model with a wrong weight almost always is, and a correct edge is never the
// one that gets changed.
func TestDetectionAndFalsePositiveRates(t *testing.T) {
	const trials = 100
	m := load(t)
	falsePos, detected, wrongEdge := 0, 0, 0
	for seed := uint64(1); seed <= trials; seed++ {
		if len(Analyze(m, seeded(seed, 12, 1, 0.03), Options{}).Suggestions) > 0 {
			falsePos++
		}
		rep := Analyze(m, seeded(seed, 12, 2, 0.03), Options{})
		if len(rep.Suggestions) > 0 {
			detected++
			for _, e := range rep.Suggestions[0].Edges {
				if e.From == "gpus" {
					wrongEdge++
				}
			}
		}
	}
	t.Logf("false positives %d/%d, detections %d/%d, wrong-edge changes %d", falsePos, trials, detected, trials, wrongEdge)
	if falsePos > trials/10 {
		t.Errorf("a correct model was changed in %d of %d trials", falsePos, trials)
	}
	if detected < trials*8/10 {
		t.Errorf("a 2x error was found in only %d of %d trials", detected, trials)
	}
	if wrongEdge > 2 {
		t.Errorf("the correct edge was changed %d times", wrongEdge)
	}
}
