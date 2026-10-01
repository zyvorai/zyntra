// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

func server(t *testing.T) *httptest.Server {
	t.Helper()
	m, err := graph.Parse([]byte(`
name: test
kpis:
  - {id: lat, value: 400, target: 300, direction: lower}
actions:
  - {id: cache, name: Cache, effects: [{kpi: lat, change: -0.3}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	s := New(m, nil, 50*time.Millisecond, nil)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestEndpoints(t *testing.T) {
	ts := server(t)

	var g struct {
		Gaps []struct{ KPI string } `json:"gaps"`
	}
	getJSON(t, ts.URL+"/api/gaps", &g)
	if len(g.Gaps) != 1 || g.Gaps[0].KPI != "lat" {
		t.Fatalf("gaps = %+v", g)
	}

	var p struct {
		Recommendations []struct{ Action, Status string } `json:"recommendations"`
	}
	getJSON(t, ts.URL+"/api/plan", &p)
	if len(p.Recommendations) != 1 || p.Recommendations[0].Status != "pending-approval" {
		t.Fatalf("plan = %+v", p)
	}

	resp, err := http.Post(ts.URL+"/api/simulate", "application/json", strings.NewReader(`{"action":"cache"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		GapsClosed []string `json:"gaps_closed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || resp.StatusCode != 200 {
		t.Fatalf("simulate: %v %d", err, resp.StatusCode)
	}
	if len(r.GapsClosed) != 1 {
		t.Fatalf("simulate result %+v", r)
	}

	for _, body := range []string{`{"action":"nope"}`, `{}`, `not json`} {
		resp, err := http.Post(ts.URL+"/api/simulate", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestEventsStream(t *testing.T) {
	ts := server(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	pulses := 0
	for sc.Scan() && pulses < 2 {
		if strings.HasPrefix(sc.Text(), "event: pulse") {
			pulses++
		}
	}
	if pulses < 2 {
		t.Fatalf("got %d pulses", pulses)
	}
}

func TestRefreshUpdatesModel(t *testing.T) {
	m, _ := graph.Parse([]byte(`kpis: [{id: a, value: 1}]`))
	s := New(m, func(_ context.Context, m *graph.Model) error { m.KPIs[0].Value = 42; return nil }, time.Hour, nil)
	s.refreshOnce(context.Background())
	got, _ := s.snapshot()
	if got.KPIs[0].Value != 42 {
		t.Fatalf("value = %v", got.KPIs[0].Value)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}
