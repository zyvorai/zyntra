// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package api serves the KPI graph, gaps, simulations and plan over HTTP,
// plus a server-sent events "pulse" stream for the dashboard.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
)

const maxBody = 1 << 20

// RefreshFunc updates a model copy in place from live sources.
type RefreshFunc func(ctx context.Context, m *graph.Model) error

type Server struct {
	mu        sync.RWMutex
	model     *graph.Model
	refreshed time.Time
	refresh   RefreshFunc
	interval  time.Duration
	static    fs.FS
}

func New(m *graph.Model, refresh RefreshFunc, interval time.Duration, static fs.FS) *Server {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Server{model: m, refreshed: time.Now(), refresh: refresh, interval: interval, static: static}
}

func (s *Server) snapshot() (*graph.Model, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model.Clone(), s.refreshed
}

// Run refreshes the model on the configured interval until ctx is done.
func (s *Server) Run(ctx context.Context) {
	if s.refresh == nil {
		return
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		s.refreshOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) refreshOnce(ctx context.Context) {
	m, _ := s.snapshot()
	if err := s.refresh(ctx, m); err != nil {
		log.Printf("refresh: %v", err)
	}
	s.mu.Lock()
	s.model, s.refreshed = m, time.Now()
	s.mu.Unlock()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/graph", s.handleGraph)
	mux.HandleFunc("GET /api/gaps", s.handleGaps)
	mux.HandleFunc("GET /api/plan", s.handlePlan)
	mux.HandleFunc("POST /api/simulate", s.handleSimulate)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	if s.static != nil {
		mux.Handle("GET /", http.FileServerFS(s.static))
	}
	return mux
}

func (s *Server) handleGraph(w http.ResponseWriter, _ *http.Request) {
	m, at := s.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"model": m, "refreshed_at": at})
}

func (s *Server) handleGaps(w http.ResponseWriter, _ *http.Request) {
	m, _ := s.snapshot()
	g := gaps.Detect(m)
	if g == nil {
		g = []gaps.Gap{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"gaps": g, "severity_total": gaps.Total(m, nil)})
}

func (s *Server) handlePlan(w http.ResponseWriter, _ *http.Request) {
	m, _ := s.snapshot()
	recs, err := planner.Plan(m)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if recs == nil {
		recs = []planner.Recommendation{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recommendations": recs})
}

type simulateRequest struct {
	Action string        `json:"action"`
	Custom *graph.Action `json:"custom,omitempty"`
}

func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var req simulateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	m, _ := s.snapshot()
	var (
		res sim.Result
		err error
	)
	switch {
	case req.Custom != nil:
		res, err = sim.Apply(m, *req.Custom)
	case req.Action != "":
		res, err = sim.Simulate(m, req.Action)
	default:
		err = fmt.Errorf("provide action or custom")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type pulse struct {
	At            time.Time                `json:"at"`
	SeverityTotal float64                  `json:"severity_total"`
	Gaps          []gaps.Gap               `json:"gaps"`
	Top           []planner.Recommendation `json:"top"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		m, at := s.snapshot()
		recs, _ := planner.Plan(m)
		if len(recs) > 3 {
			recs = recs[:3]
		}
		b, _ := json.Marshal(pulse{At: at, SeverityTotal: gaps.Total(m, nil), Gaps: gaps.Detect(m), Top: recs})
		fmt.Fprintf(w, "event: pulse\ndata: %s\n\n", b)
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
