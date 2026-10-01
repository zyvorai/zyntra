// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/planner"
)

const model = `
kpis:
  - {id: headroom, name: Capacity headroom, unit: "%", value: 10, target: 20, direction: higher}
  - {id: wait, name: Queue wait, unit: min, value: 30, target: 15, direction: lower}
  - {id: cost, name: Spend, value: 100, target: 150, direction: lower}
edges:
  - {from: headroom, to: wait, weight: -0.5, why: free GPUs drain the queue}
actions:
  - {id: spot, name: Move batch to spot, risk: medium, effects: [{kpi: headroom, change: 1}]}
`

func load(t *testing.T) *graph.Model {
	t.Helper()
	m, err := graph.Parse([]byte(model))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func snapshot(t *testing.T, m *graph.Model, h *History) Snapshot {
	t.Helper()
	plan, err := planner.Plan(m)
	if err != nil {
		t.Fatal(err)
	}
	return Snapshot{Model: m, Gaps: gaps.Detect(m), Severity: gaps.Total(m, nil), Plan: plan,
		Anomalies: Anomalies(m, h), Forecasts: Forecasts(m, h)}
}

func TestAnomaly(t *testing.T) {
	m := load(t)
	h := NewHistory(100)
	t0 := time.Unix(0, 0)
	for i := 0; i < 10; i++ {
		m.KPIs[2].Value = 100 + float64(i%2)
		h.Record(m, t0.Add(time.Duration(i)*time.Minute))
	}
	if a := Anomalies(m, h); len(a) != 0 {
		t.Fatalf("unexpected anomalies %+v", a)
	}
	m.KPIs[2].Value = 140
	h.Record(m, t0.Add(11*time.Minute))
	a := Anomalies(m, h)
	if len(a) != 1 || a[0].KPI != "cost" || a[0].Z < 3 || a[0].Severity != "critical" {
		t.Fatalf("anomalies = %+v", a)
	}
}

func TestForecast(t *testing.T) {
	m := load(t)
	h := NewHistory(100)
	t0 := time.Unix(0, 0)
	for i := 0; i < 6; i++ {
		m.KPIs[2].Value = 90 + float64(i)*10 // +10 per hour, ends at 140 toward 150
		h.Record(m, t0.Add(time.Duration(i)*time.Hour))
	}
	var cost Forecast
	for _, f := range Forecasts(m, h) {
		if f.KPI == "cost" {
			cost = f
		}
	}
	if cost.Status != "at-risk" || cost.ETASeconds == nil {
		t.Fatalf("cost forecast %+v", cost)
	}
	if eta := *cost.ETASeconds / 3600; eta < 0.99 || eta > 1.01 {
		t.Fatalf("eta = %.3f h, want 1h (150 - 140) / 10", eta)
	}
}

func TestAskIntents(t *testing.T) {
	m := load(t)
	s := snapshot(t, m, NewHistory(10))
	e := &Engine{}
	cases := map[string]string{
		"what's wrong right now?":         "gaps",
		"what should we do?":              "plan",
		"what if we move batch to spot?":  "simulate",
		"why is queue wait so high":       "why",
		"any anomalies?":                  "anomalies",
		"when will we breach? forecast":   "forecast",
		"are netra and gravia connected?": "sources",
		"hello":                           "summary",
	}
	for q, want := range cases {
		a := e.Ask(context.Background(), q, s)
		if a.Intent != want {
			t.Errorf("%q: intent %s, want %s (text %q)", q, a.Intent, want, a.Text)
		}
		if a.Mode != "heuristic" || a.Text == "" {
			t.Errorf("%q: %+v", q, a)
		}
	}
	plan := e.Ask(context.Background(), "what should we do?", s)
	if !strings.Contains(plan.Text, "Move batch to spot") || !strings.Contains(plan.Text, "approval") {
		t.Fatalf("plan answer %q", plan.Text)
	}
	why := e.Ask(context.Background(), "why is queue wait high", s)
	if !strings.Contains(why.Text, "free GPUs drain the queue") {
		t.Fatalf("why answer %q", why.Text)
	}
}

func TestLLMRewriteAndFallback(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>x</think>Rewritten answer."}}]}`))
	}))
	defer srv.Close()
	m := load(t)
	s := snapshot(t, m, NewHistory(10))
	e := &Engine{LLM: NewProvider(srv.URL, "fvai_test", "demo-qwen", "fabric", false)}
	a := e.Ask(context.Background(), "what should we do?", s)
	if a.Mode != "llm" || a.Text != "Rewritten answer." || a.Model != "demo-qwen" {
		t.Fatalf("answer %+v", a)
	}
	if gotAuth != "Bearer fvai_test" || gotBody["model"] != "demo-qwen" {
		t.Fatalf("request auth=%q body=%v", gotAuth, gotBody)
	}
	if st := e.Status(); st.Mode != "llm-rewrite" || st.Provider != "fabric" {
		t.Fatalf("status %+v", st)
	}

	srv.Close()
	a = e.Ask(context.Background(), "what should we do?", s)
	if a.Mode != "heuristic" || a.LLMError == "" || !strings.Contains(a.Text, "Move batch to spot") {
		t.Fatalf("fallback %+v", a)
	}
}

func TestDigestAndExplain(t *testing.T) {
	m := load(t)
	s := snapshot(t, m, NewHistory(10))
	d := Digest(s)
	if !strings.Contains(d.Text, "off target") || !strings.Contains(d.Text, "pending approval") {
		t.Fatalf("digest %q", d.Text)
	}
	x := Explain(s.Plan[0].Result)
	if !strings.Contains(x.Text, "headroom moves wait") || !strings.Contains(x.Text, "closes") {
		t.Fatalf("explain %q", x.Text)
	}
}
