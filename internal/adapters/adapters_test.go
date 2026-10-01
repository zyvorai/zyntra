// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters/httpsrc"
	"github.com/zyvorai/zyntra/internal/adapters/prometheus"
	"github.com/zyvorai/zyntra/internal/graph"
)

const nodesJSON = `{"items":[
 {"status":{"allocatable":{"cpu":"7500m","nvidia.com/gpu":"4"},"conditions":[{"type":"Ready","status":"True"}]}},
 {"status":{"allocatable":{"cpu":"8","nvidia.com/gpu":"4"},"conditions":[{"type":"Ready","status":"False"}]}}
]}`

func TestRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" || r.URL.Query().Get("query") != "up_latency" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"312.5"]}]}}`))
	}))
	defer srv.Close()

	m, err := graph.Parse([]byte(`
kpis:
  - {id: lat, value: 1, source: {kind: prometheus, query: up_latency}}
  - {id: gpus, value: 1, source: {kind: kubernetes, metric: gpu_allocatable}}
  - {id: ready, value: 1, source: {kind: kubernetes, metric: nodes_ready}}
  - {id: cpu, value: 1, source: {kind: kubernetes, metric: cpu_allocatable}}
  - {id: static, value: 7}
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Prometheus: prometheus.New(srv.URL),
		Kubernetes: func(context.Context) ([]byte, error) { return []byte(nodesJSON), nil },
	}
	if _, err := Refresh(context.Background(), m, cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"lat": 312.5, "gpus": 8, "ready": 1, "cpu": 15.5, "static": 7}
	for id, v := range want {
		if k, _ := m.KPI(id); k.Value != v {
			t.Errorf("%s = %v, want %v", id, k.Value, v)
		}
	}
}

func TestRefreshReportsMissingMetric(t *testing.T) {
	m, err := graph.Parse([]byte(`
kpis:
  - {id: x, value: 3, source: {kind: kubernetes, metric: nope}}
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Kubernetes: func(context.Context) ([]byte, error) { return []byte(nodesJSON), nil }}
	if _, err := Refresh(context.Background(), m, cfg); err == nil {
		t.Fatal("expected error for missing metric")
	}
	if k, _ := m.KPI("x"); k.Value != 3 {
		t.Fatal("value should be unchanged on error")
	}
}

func TestRateTracker(t *testing.T) {
	r := NewRateTracker()
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	if _, ok := r.Observe("x", 100); ok {
		t.Fatal("first sample should not yield a rate")
	}
	now = now.Add(10 * time.Second)
	if v, ok := r.Observe("x", 150); !ok || v != 5 {
		t.Fatalf("rate = %v %v", v, ok)
	}
	now = now.Add(10 * time.Second)
	if v, ok := r.Observe("x", 20); !ok || v != 2 {
		t.Fatalf("after reset rate = %v %v", v, ok)
	}
}

func TestHTTPSourcesAndHealth(t *testing.T) {
	scrapes := 0
	netra := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer nk" {
			http.Error(w, "no", 401)
			return
		}
		switch r.URL.Path {
		case "/metrics":
			scrapes++
			w.Write([]byte("netra_dns_failures{node=\"a\"} 2\nnetra_dns_failures{node=\"b\"} 3\nnetra_agents_stale 0\n"))
		case "/api/v1/ebpf/health":
			w.Write([]byte(`{"attached": true, "programs": [{"ok":true},{"ok":false}]}`))
		}
	}))
	defer netra.Close()
	logins := 0
	fabric := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			logins++
			w.Write([]byte(`{"token":"jwt"}`))
		case r.Header.Get("Authorization") != "Bearer jwt":
			http.Error(w, "expired", 401)
		default:
			w.Write([]byte(`{"vms":[{"state":"running"},{"state":"error"},{"state":"running"}]}`))
		}
	}))
	defer fabric.Close()

	m, err := graph.Parse([]byte(`
kpis:
  - {id: dns, value: 0, source: {kind: metrics, endpoint: netra, metric: netra_dns_failures}}
  - {id: stale, value: 9, source: {kind: metrics, endpoint: netra, metric: netra_agents_stale}}
  - {id: attached, value: 0, source: {kind: netra, path: /api/v1/ebpf/health, field: attached}}
  - {id: vms_running, value: 0, source: {kind: fabric, path: /api/vms, field: "vms.#(state=running)"}}
  - {id: pct, value: 0, source: {kind: fabric, path: /api/vms, field: "vms.#", scale: 10}}
  - {id: gone, value: 4, source: {kind: gravia, path: /x, field: y}}
`))
	if err != nil {
		t.Fatal(err)
	}
	fc := httpsrc.New("fabric", fabric.URL, false).WithLogin(func(ctx context.Context, c *httpsrc.Client) (string, error) {
		var out struct{ Token string }
		if err := c.PostJSONAnon(ctx, "/login", map[string]string{}, &out); err != nil {
			return "", err
		}
		return out.Token, nil
	})
	cfg := Config{Endpoints: map[string]*httpsrc.Client{
		"netra":  httpsrc.New("netra", netra.URL, false).WithBearer("nk"),
		"fabric": fc,
	}}
	status, err := Refresh(context.Background(), m, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"dns": 5, "stale": 0, "attached": 1, "vms_running": 2, "pct": 30, "gone": 4}
	for id, v := range want {
		if k, _ := m.KPI(id); k.Value != v {
			t.Errorf("%s = %v, want %v", id, k.Value, v)
		}
	}
	if scrapes != 1 {
		t.Errorf("metrics endpoint scraped %d times, want 1", scrapes)
	}
	if logins != 1 {
		t.Errorf("fabric logins = %d, want 1", logins)
	}
	if len(status) != 2 || status[0].Name != "fabric" || !status[0].OK || len(status[1].KPIs) != 3 {
		t.Fatalf("status = %+v", status)
	}

	cfg.Endpoints["netra"] = httpsrc.New("netra", netra.URL, false).WithBearer("wrong")
	status, err = Refresh(context.Background(), m, cfg)
	if err == nil || status[1].OK || status[1].Error == "" {
		t.Fatalf("expected netra failure, status=%+v err=%v", status, err)
	}
}
