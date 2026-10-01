// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"
)

// fakeSources serves Netra-, Gravia-, Fabric- and Keep-shaped endpoints on
// one listener so the lab model can run anywhere:
//
//	ZYNTRA_NETRA_URL=http://ADDR ZYNTRA_GRAVIA_URL=http://ADDR
//	ZYNTRA_FABRIC_URL=http://ADDR ZYNTRA_KEEP_URL=http://ADDR
func fakeSources(ctx context.Context, addr string) error {
	start := time.Now()
	elapsed := func() float64 { return time.Since(start).Seconds() }
	wave := func(period float64) float64 { return math.Sin(2 * math.Pi * elapsed() / period) }
	js := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		t := elapsed()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE netra_tcp_retransmissions counter\n")
		fmt.Fprintf(w, "netra_tcp_retransmissions{node=\"lab-1\"} %.0f\n", 1000+3*t)
		fmt.Fprintf(w, "netra_tcp_retransmissions{node=\"lab-2\"} %.0f\n", 500+1.5*t)
		fmt.Fprintf(w, "# TYPE netra_kernel_skb_drops counter\nnetra_kernel_skb_drops{node=\"lab-1\"} %.0f\n", 200+2*t)
		fmt.Fprintf(w, "# TYPE netra_tcp_resets counter\nnetra_tcp_resets{node=\"lab-1\"} %.0f\n", 1e6+120*t)
		fmt.Fprintf(w, "# TYPE netra_tcp_average_srtt_us gauge\nnetra_tcp_average_srtt_us %.0f\n", 4200+600*wave(300))
		fmt.Fprintf(w, "# TYPE netra_tcp_connect_average_latency_us gauge\nnetra_tcp_connect_average_latency_us %.0f\n", 7000+1500*wave(240))
		fmt.Fprintf(w, "# TYPE netra_agents_stale gauge\nnetra_agents_stale 0\n")
	})
	mux.HandleFunc("GET /api/v1/ebpf/health", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"summary": map[string]any{"healthScore": 55 + 5*wave(600), "tcpRetransmissions": 1780, "averageSrttUs": 4149}})
	})
	mux.HandleFunc("GET /api/cluster/stats", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"totalGPUs": 8, "availableGPUs": 1, "allocatedGPUs": 7, "utilizationPercent": 71 + 6*wave(400),
			"pendingJobs": 3, "runningJobs": 9, "totalNodes": 2})
	})
	mux.HandleFunc("GET /api/metrics/costs", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"totalCost": 1830.5 + elapsed()/60})
	})
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"token": "fake-fabric-jwt", "username": "admin", "role": "admin"})
	})
	mux.HandleFunc("GET /api/v1/system/metrics", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"cpu_percent": 38 + 10*wave(180), "memory_percent": 64 + 3*wave(900)})
	})
	mux.HandleFunc("GET /api/v1/system/info", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{
			"cpu":         map[string]any{"core_count": 12, "load_avg_1": 6 + 2*wave(200), "usage_percent": 38},
			"filesystems": []any{map[string]any{"mountpoint": "/", "usage_percent": 19.1}, map[string]any{"mountpoint": "/dev/shm", "usage_percent": 0}},
		})
	})
	mux.HandleFunc("GET /api/v1/system/containers", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"summary": map[string]any{"running": 3, "total": 4}, "containers": []any{}})
	})
	mux.HandleFunc("GET /v1/keep/status", func(w http.ResponseWriter, _ *http.Request) {
		js(w, map[string]any{"keep_mode": true, "signature_required": true, "trusted_signers": 1, "fluxvm": map[string]any{"ready": true}})
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("fake sources on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
