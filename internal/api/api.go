// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package api serves Zyntra's REST API, the server-sent "pulse" stream and
// the embedded web console.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/adapters"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/sim"
)

const maxBody = 1 << 20

// RefreshFunc updates a model copy in place from live sources.
type RefreshFunc func(ctx context.Context, m *graph.Model) ([]adapters.Status, error)

// Approval modes.
const (
	ModeLocal = "local"
	ModeKeep  = "keep"
)

// KeepBridge is the slice of Fabric Keep that Zyntra uses.
type KeepBridge interface {
	// Status reports whether Keep is reachable and how it is configured.
	Status(ctx context.Context) (map[string]any, error)
	// Start opens a Keep session that asks Keep to approve and run p by
	// calling execURL through Keep's broker.
	Start(ctx context.Context, p approvals.Proposal, execURL string) (approvals.KeepRef, error)
	// AwaitApproval waits for the Keep approval raised by a session.
	AwaitApproval(ctx context.Context, sessionID string) (string, error)
	// Decide approves or denies a Keep approval.
	Decide(ctx context.Context, approvalID string, approve bool) error
	// Mirror records a local decision/execution in Keep's audit chain.
	Mirror(ctx context.Context, p approvals.Proposal) (approvals.KeepRef, error)
	Approvals(ctx context.Context) (any, error)
	Receipts(ctx context.Context, sessionID string) (any, error)
	Audit(ctx context.Context, sessionID string) (any, error)
}

type Options struct {
	Model    *graph.Model
	Refresh  RefreshFunc
	Interval time.Duration
	Static   fs.FS
	Auth     *auth.Auth
	AI       *ai.Engine
	History  *ai.History
	Store    *approvals.Store
	Executor *executor.Executor
	Keep     KeepBridge
	// ApprovalMode is local or keep.
	ApprovalMode string
	// KeepDoubleApproval leaves the Keep approval for a second human in
	// Fabric's Keep console instead of deciding it on the approver's behalf.
	KeepDoubleApproval bool
	// ExecURL is how Keep's broker reaches this server, e.g.
	// http://212.8.248.187:19620 (the proposal path is appended).
	ExecURL  string
	StateDir string
	Version  string
	Host     string
}

type Server struct {
	opt Options

	mu        sync.RWMutex
	model     *graph.Model
	sources   []adapters.Status
	refreshed time.Time

	bg sync.WaitGroup
}

func New(o Options) *Server {
	if o.Interval <= 0 {
		o.Interval = 15 * time.Second
	}
	if o.Auth == nil {
		o.Auth = auth.New("", "")
	}
	if o.AI == nil {
		o.AI = &ai.Engine{}
	}
	if o.History == nil {
		o.History = ai.NewHistory(0)
	}
	if o.Store == nil {
		o.Store, _ = approvals.Open("")
	}
	if o.Executor == nil {
		o.Executor = &executor.Executor{Mode: executor.ModeDryRun}
	}
	if o.ApprovalMode == "" {
		o.ApprovalMode = ModeLocal
	}
	s := &Server{opt: o, model: o.Model, refreshed: time.Now()}
	s.loadHistory()
	if o.Refresh == nil {
		o.History.Record(o.Model, time.Now())
	}
	return s
}

func (s *Server) snapshot() (*graph.Model, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model.Clone(), s.refreshed
}

func (s *Server) sourceStatus() []adapters.Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]adapters.Status(nil), s.sources...)
}

func (s *Server) aiSnapshot() ai.Snapshot {
	m, _ := s.snapshot()
	plan, _ := planner.Plan(m)
	return ai.Snapshot{
		Model: m, Gaps: gaps.Detect(m), Severity: gaps.Total(m, nil), Plan: plan,
		Anomalies: ai.Anomalies(m, s.opt.History), Forecasts: ai.Forecasts(m, s.opt.History),
		Sources: s.sourceStatus(),
	}
}

// Run refreshes the model and records history every interval until ctx ends.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(s.opt.Interval)
	defer t.Stop()
	saveEvery := 0
	for {
		s.RefreshOnce(ctx)
		if saveEvery++; saveEvery%20 == 0 {
			s.saveHistory()
		}
		select {
		case <-ctx.Done():
			s.saveHistory()
			s.bg.Wait()
			return
		case <-t.C:
		}
	}
}

// Wait blocks until background Keep work has finished.
func (s *Server) Wait() { s.bg.Wait() }

func (s *Server) RefreshOnce(ctx context.Context) {
	m, _ := s.snapshot()
	var st []adapters.Status
	if s.opt.Refresh != nil {
		var err error
		st, err = s.opt.Refresh(ctx, m)
		if err != nil {
			log.Printf("refresh: %v", err)
		}
	}
	now := time.Now()
	s.mu.Lock()
	s.model, s.refreshed = m, now
	if s.opt.Refresh != nil {
		s.sources = st
	}
	s.mu.Unlock()
	s.opt.History.Record(m, now)
}

func (s *Server) historyPath() string {
	if s.opt.StateDir == "" {
		return ""
	}
	return filepath.Join(s.opt.StateDir, "history.json")
}

func (s *Server) loadHistory() {
	p := s.historyPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var d map[string][]ai.Point
	if json.Unmarshal(b, &d) == nil {
		s.opt.History.Restore(d)
	}
}

func (s *Server) saveHistory() {
	p := s.historyPath()
	if p == "" {
		return
	}
	b, err := json.Marshal(s.opt.History.Snapshot())
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o750)
	if err := os.WriteFile(p+".tmp", b, 0o640); err == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	admin := func(h http.HandlerFunc) http.Handler { return s.opt.Auth.Require(h, auth.RoleAdmin) }

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/meta", s.handleMeta)
	s.opt.Auth.Routes(mux)

	mux.Handle("GET /api/v1/graph", admin(s.handleGraph))
	mux.Handle("GET /api/v1/gaps", admin(s.handleGaps))
	mux.Handle("GET /api/v1/plan", admin(s.handlePlan))
	mux.Handle("POST /api/v1/simulate", admin(s.handleSimulate))
	mux.Handle("GET /api/v1/sources", admin(s.handleSources))
	mux.Handle("GET /api/v1/events", admin(s.handleEvents))
	mux.Handle("GET /api/v1/kpis/{id}/history", admin(s.handleKPIHistory))

	mux.Handle("GET /api/v1/ai/status", admin(s.handleAIStatus))
	mux.Handle("GET /api/v1/ai/digest", admin(s.handleAIDigest))
	mux.Handle("GET /api/v1/ai/insights", admin(s.handleAIInsights))
	mux.Handle("POST /api/v1/ai/ask", admin(s.handleAIAsk))
	mux.Handle("POST /api/v1/ai/explain", admin(s.handleAIExplain))

	mux.Handle("GET /api/v1/proposals", admin(s.handleProposals))
	mux.Handle("POST /api/v1/proposals", admin(s.handlePropose))
	mux.Handle("GET /api/v1/proposals/{id}", admin(s.handleProposal))
	mux.Handle("POST /api/v1/proposals/{id}/approve", admin(s.handleApprove))
	mux.Handle("POST /api/v1/proposals/{id}/reject", admin(s.handleReject))
	mux.Handle("GET /api/v1/audit", admin(s.handleAudit))
	mux.Handle("POST /api/v1/exec/{id}", s.opt.Auth.Require(http.HandlerFunc(s.handleExec), auth.RoleExec, auth.RoleAdmin))

	mux.Handle("GET /api/v1/keep/status", admin(s.handleKeepStatus))
	mux.Handle("GET /api/v1/keep/approvals", admin(s.handleKeepApprovals))
	mux.Handle("POST /api/v1/keep/approvals/{id}", admin(s.handleKeepDecide))
	mux.Handle("GET /api/v1/keep/receipts", admin(s.handleKeepReceipts))
	mux.Handle("GET /api/v1/keep/audit", admin(s.handleKeepAudit))

	if s.opt.Static != nil {
		mux.Handle("GET /", spa(s.opt.Static))
	}
	return mux
}

// ExecHandler serves only the exec endpoint, for the loopback TLS listener
// that Fabric Keep's broker calls.
func (s *Server) ExecHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /api/v1/exec/{id}", s.opt.Auth.Require(http.HandlerFunc(s.handleExec), auth.RoleExec, auth.RoleAdmin))
	return mux
}

// spa serves static files and falls back to index.html for client routes.
func spa(static fs.FS) http.Handler {
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "api/") {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		if p == "" || p == "." {
			p = "index.html"
		}
		if _, err := fs.Stat(static, p); err != nil {
			p = "index.html"
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if p == "index.html" {
			b, err := fs.ReadFile(static, "index.html")
			if err != nil {
				http.Error(w, "web console not built (run make web)", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(b)
			return
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	src := s.sourceStatus()
	healthy := 0
	for _, x := range src {
		if x.OK {
			healthy++
		}
	}
	m, _ := s.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"product": "Zyntra", "version": s.opt.Version, "host": s.opt.Host, "model": m.Name,
		"auth_required": s.opt.Auth.Required(),
		"sources":       map[string]int{"total": len(src), "healthy": healthy},
		"approval_mode": s.opt.ApprovalMode, "execute_mode": s.opt.Executor.Mode,
		"ai_mode": s.opt.AI.Status().Mode,
	})
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
		writeErr(w, http.StatusInternalServerError, err.Error())
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
	if !decode(w, r, &req) {
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
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSources(w http.ResponseWriter, _ *http.Request) {
	src := s.sourceStatus()
	if src == nil {
		src = []adapters.Status{}
	}
	_, at := s.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{"sources": src, "refreshed_at": at})
}

func (s *Server) handleKPIHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, _ := s.snapshot()
	if _, ok := m.KPI(id); !ok {
		writeErr(w, http.StatusNotFound, "unknown kpi")
		return
	}
	pts := s.opt.History.Series(id)
	if pts == nil {
		pts = []ai.Point{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"kpi": id, "points": pts})
}

type pulse struct {
	At            time.Time                `json:"at"`
	SeverityTotal float64                  `json:"severity_total"`
	Gaps          []gaps.Gap               `json:"gaps"`
	Top           []planner.Recommendation `json:"top"`
	Anomalies     int                      `json:"anomalies"`
	Sources       []adapters.Status        `json:"sources"`
	Pending       int                      `json:"pending_approvals"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	t := time.NewTicker(s.opt.Interval)
	defer t.Stop()
	for {
		snap := s.aiSnapshot()
		_, at := s.snapshot()
		top := snap.Plan
		if len(top) > 3 {
			top = top[:3]
		}
		pending := 0
		for _, p := range s.opt.Store.List() {
			if p.Status == approvals.Pending {
				pending++
			}
		}
		g := snap.Gaps
		if g == nil {
			g = []gaps.Gap{}
		}
		b, _ := json.Marshal(pulse{At: at, SeverityTotal: snap.Severity, Gaps: g, Top: top,
			Anomalies: len(snap.Anomalies), Sources: snap.Sources, Pending: pending})
		fmt.Fprintf(w, "event: pulse\ndata: %s\n\n", b)
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) handleAIStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.opt.AI.Status())
}

func (s *Server) handleAIDigest(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opt.AI.Digest(r.Context(), s.aiSnapshot()))
}

func (s *Server) handleAIInsights(w http.ResponseWriter, _ *http.Request) {
	snap := s.aiSnapshot()
	an, fc := snap.Anomalies, snap.Forecasts
	if an == nil {
		an = []ai.Anomaly{}
	}
	if fc == nil {
		fc = []ai.Forecast{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"anomalies": an, "forecasts": fc})
}

func (s *Server) handleAIAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	if !decode(w, r, &req) {
		return
	}
	q := strings.TrimSpace(req.Question)
	if q == "" || len(q) > 2000 {
		writeErr(w, http.StatusBadRequest, "question must be 1-2000 characters")
		return
	}
	writeJSON(w, http.StatusOK, s.opt.AI.Ask(r.Context(), q, s.aiSnapshot()))
}

func (s *Server) handleAIExplain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &req) {
		return
	}
	snap := s.aiSnapshot()
	res, err := sim.Simulate(snap.Model, req.Action)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.opt.AI.Explain(r.Context(), res, snap))
}

func (s *Server) handleProposals(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"proposals": s.opt.Store.List(), "approval_mode": s.opt.ApprovalMode,
		"execute_mode": s.opt.Executor.Mode, "double_approval": s.opt.KeepDoubleApproval,
	})
}

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, _ := s.snapshot()
	a, ok := m.Action(req.Action)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	res, err := sim.Apply(m, *a)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p := approvals.Proposal{
		Action: a.ID, ActionName: a.Name, Risk: string(a.Risk), Adapter: a.Adapter,
		Predicted: approvals.Prediction{SeverityBefore: res.SeverityBefore, SeverityAfter: res.SeverityAfter,
			Closes: res.GapsClosed, Opens: res.GapsOpened, KPIs: map[string]float64{}},
		Baseline: map[string]float64{},
	}
	for _, k := range res.KPIs {
		if k.Change != 0 {
			p.Predicted.KPIs[k.KPI] = k.After
			p.Baseline[k.KPI] = k.Before
		}
	}
	if a.Execute != nil {
		p.Template = a.Execute.Template
		if rd, err := executor.Render(*a); err != nil {
			p.RenderErr = err.Error()
		} else {
			p.Render = rd.Display
		}
	}
	out, created, err := s.opt.Store.Create(p, auth.FromContext(r.Context()).Subject)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, out)
}

func (s *Server) handleProposal(w http.ResponseWriter, r *http.Request) {
	p, err := s.opt.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleAudit(w http.ResponseWriter, _ *http.Request) {
	ev := s.opt.Store.Audit()
	if ev == nil {
		ev = []approvals.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

type decision struct {
	Reason string `json:"reason"`
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	var d decision
	if !decodeOptional(w, r, &d) {
		return
	}
	p, err := s.opt.Store.Decide(r.PathValue("id"), false, auth.FromContext(r.Context()).Subject, d.Reason)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	s.mirror(p.ID)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var d decision
	if !decodeOptional(w, r, &d) {
		return
	}
	id := r.PathValue("id")
	cur, err := s.opt.Store.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if cur.Status == approvals.Pending && cur.Template != "" && cur.RenderErr != "" {
		writeErr(w, http.StatusUnprocessableEntity, "cannot approve: "+cur.RenderErr)
		return
	}
	who := auth.FromContext(r.Context()).Subject
	p, err := s.opt.Store.Decide(id, true, who, d.Reason)
	if err != nil {
		writeErr(w, statusFor(err), err.Error())
		return
	}
	if p.Template == "" {
		writeJSON(w, http.StatusOK, p)
		return
	}
	if s.opt.ApprovalMode == ModeKeep && s.opt.Keep != nil {
		ref, err := s.opt.Keep.Start(r.Context(), p, strings.TrimRight(s.opt.ExecURL, "/")+"/api/v1/exec/"+p.ID)
		if err != nil {
			log.Printf("keep start %s: %v; executing locally", p.ID, err)
			ref = approvals.KeepRef{Mode: ModeKeep, Error: err.Error()}
			p, _ = s.opt.Store.Update(p.ID, func(x *approvals.Proposal) { x.Keep = &ref })
			p = s.execute(r.Context(), p.ID, "zyntra (keep unavailable)")
			s.mirror(p.ID)
			writeJSON(w, http.StatusOK, p)
			return
		}
		p, _ = s.opt.Store.Update(p.ID, func(x *approvals.Proposal) { x.Keep = &ref })
		if !s.opt.KeepDoubleApproval {
			s.bg.Add(1)
			go s.confirmKeep(p.ID, ref.SessionID)
		}
		writeJSON(w, http.StatusAccepted, p)
		return
	}
	p = s.execute(r.Context(), p.ID, who)
	s.mirror(p.ID)
	writeJSON(w, http.StatusOK, p)
}

// confirmKeep decides the Keep approval raised for a session on behalf of
// the human who already approved in Zyntra.
func (s *Server) confirmKeep(id, session string) {
	defer s.bg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	aid, err := s.opt.Keep.AwaitApproval(ctx, session)
	if err == nil {
		err = s.opt.Keep.Decide(ctx, aid, true)
	}
	p, _ := s.opt.Store.Update(id, func(x *approvals.Proposal) {
		if x.Keep == nil {
			x.Keep = &approvals.KeepRef{Mode: ModeKeep, SessionID: session}
		}
		if aid != "" {
			x.Keep.ApprovalID = aid
		}
		if err != nil {
			x.Keep.Error = err.Error()
		}
	})
	if err == nil {
		return
	}
	log.Printf("keep confirm %s: %v", id, err)
	// Keep never ran the call; the human approval stands, so run it here.
	if aid == "" && p.Status == approvals.Approved {
		s.execute(ctx, id, "zyntra (keep unavailable)")
		s.mirrorNow(id)
	}
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.opt.Store.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if p.Status != approvals.Approved {
		writeErr(w, http.StatusConflict, "proposal is "+string(p.Status)+", not approved")
		return
	}
	p = s.execute(r.Context(), id, auth.FromContext(r.Context()).Subject)
	code := http.StatusOK
	if p.Status == approvals.Failed {
		code = http.StatusBadGateway
	}
	writeJSON(w, code, p)
}

// execute renders and runs an approved proposal, then records the KPIs that
// the prediction covered so predicted and actual can be compared.
func (s *Server) execute(ctx context.Context, id, by string) approvals.Proposal {
	p, _ := s.opt.Store.Get(id)
	m, _ := s.snapshot()
	a, ok := m.Action(p.Action)
	var res executor.Result
	if !ok {
		res = executor.Result{Mode: s.opt.Executor.Mode, Error: "action no longer in model"}
	} else if rd, err := executor.Render(*a); err != nil {
		res = executor.Result{Mode: s.opt.Executor.Mode, Error: err.Error()}
	} else {
		res = s.opt.Executor.Execute(ctx, rd)
	}
	out, err := s.opt.Store.Complete(id, res, by)
	if err != nil {
		out, _ = s.opt.Store.Get(id)
		return out
	}
	if res.OK && res.Mode == executor.ModeApply {
		s.RefreshOnce(ctx)
		m, _ = s.snapshot()
		actual := map[string]float64{}
		for kid := range out.Predicted.KPIs {
			if k, ok := m.KPI(kid); ok {
				actual[kid] = k.Value
			}
		}
		_ = s.opt.Store.RecordActual(id, actual)
		out, _ = s.opt.Store.Get(id)
	}
	return out
}

// mirror copies a local decision into Keep's audit chain in the background.
func (s *Server) mirror(id string) {
	if s.opt.Keep == nil {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		s.mirrorNow(id)
	}()
}

func (s *Server) mirrorNow(id string) {
	if s.opt.Keep == nil {
		return
	}
	p, err := s.opt.Store.Get(id)
	if err != nil || (p.Keep != nil && p.Keep.Mode == ModeKeep && p.Keep.Error == "") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ref, err := s.opt.Keep.Mirror(ctx, p)
	if err != nil {
		ref.Mode, ref.Error = "mirror", err.Error()
		log.Printf("keep mirror %s: %v", id, err)
	}
	if p.Keep != nil && p.Keep.Error != "" {
		ref.Error = strings.TrimPrefix(ref.Error+"; ", "; ") + "keep execute: " + p.Keep.Error
	}
	_, _ = s.opt.Store.Update(id, func(x *approvals.Proposal) { x.Keep = &ref })
}

func (s *Server) keep(w http.ResponseWriter) bool {
	if s.opt.Keep == nil {
		writeErr(w, http.StatusServiceUnavailable, "Fabric Keep is not configured")
		return false
	}
	return true
}

func (s *Server) handleKeepStatus(w http.ResponseWriter, r *http.Request) {
	if s.opt.Keep == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "approval_mode": s.opt.ApprovalMode})
		return
	}
	st, err := s.opt.Keep.Status(r.Context())
	out := map[string]any{"configured": true, "approval_mode": s.opt.ApprovalMode, "double_approval": s.opt.KeepDoubleApproval, "status": st}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleKeepApprovals(w http.ResponseWriter, r *http.Request) {
	if !s.keep(w) {
		return
	}
	v, err := s.opt.Keep.Approvals(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleKeepDecide(w http.ResponseWriter, r *http.Request) {
	if !s.keep(w) {
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Decision != "approved" && req.Decision != "denied" {
		writeErr(w, http.StatusBadRequest, "decision must be approved or denied")
		return
	}
	if err := s.opt.Keep.Decide(r.Context(), r.PathValue("id"), req.Decision == "approved"); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleKeepReceipts(w http.ResponseWriter, r *http.Request) {
	if !s.keep(w) {
		return
	}
	v, err := s.opt.Keep.Receipts(r.Context(), r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleKeepAudit(w http.ResponseWriter, r *http.Request) {
	if !s.keep(w) {
		return
	}
	v, err := s.opt.Keep.Audit(r.Context(), r.URL.Query().Get("session"))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func statusFor(err error) int {
	if errors.Is(err, approvals.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusConflict
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// decodeOptional accepts an empty body.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
