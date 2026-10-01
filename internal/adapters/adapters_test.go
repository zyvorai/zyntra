// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package adapters

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if err := Refresh(context.Background(), m, cfg); err != nil {
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
	if err := Refresh(context.Background(), m, cfg); err == nil {
		t.Fatal("expected error for missing metric")
	}
	if k, _ := m.KPI("x"); k.Value != 3 {
		t.Fatal("value should be unchanged on error")
	}
}
