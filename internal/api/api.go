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
	"github.com/zyvorai/zyntra/internal/decisions"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/inputs"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/policy"
	"github.com/zyvorai/zyntra/internal/sim"
)

const maxBody = 1 << 20

// RefreshFunc updates a model copy in place from live sources.
type RefreshFunc func(ctx context.Context, m *graph.Model) (adapters.Report, error)

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
	// Inputs holds webhook-in documents and manual KPI values.
	Inputs *inputs.Store
	// Ontology is the pack's business-object layer; the zero value means
	// the pack has none.
	Ontology OntologyOptions
	// Policy sets approval quorums, expiry, maintenance windows and Keep
	// requirements; nil keeps the defaults.
	Policy *policy.Policy
	// Signer signs decision exports; nil uses a key in StateDir (or an
	// ephemeral one without a StateDir).
	Signer *decisions.Signer
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
	fresh     *freshness.Tracker

	bg sync.WaitGroup
}

// freshness returns the current freshness of every KPI in m. Without any
// live refresh the model's values are all there is, so they count as static.
func (s *Server) freshness(m *graph.Model) []freshness.State {
	if s.opt.Refresh == nil {
		return (*freshness.Tracker)(nil).States(m)
	}
	return s.fresh.States(m)
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
	if o.Policy == nil {
		o.Policy, _ = policy.Load("")
	}
	o.Policy.UseModel(o.Model)
	if o.Inputs == nil {
		o.Inputs, _ = inputs.Open("")
	}
	if o.Signer == nil {
		path := ""
		if o.StateDir != "" {
			path = filepath.Join(o.StateDir, "decision-signing.key")
		}
		signer, err := decisions.LoadSigner(path)
		if err != nil {
			log.Printf("decision signing key: %v; using an ephemeral key", err)
			signer, _ = decisions.LoadSigner("")
		}
		o.Signer = signer
	}
	s := &Server{opt: o, model: o.Model, refreshed: time.Now(), fresh: freshness.New(3 * o.Interval)}
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

func (s *Server) aiSnapshot(owner ...string) ai.Snapshot {
	m, _ := s.snapshot()
	plan, _, _ := s.plan(m)
	g := gaps.Detect(m)
	if len(owner) > 0 && owner[0] != "" {
		plan = planner.ForOwner(m, plan, owner[0])
		g = gaps.ForOwner(g, owner[0])
	}
	return ai.Snapshot{
		Model: m, Gaps: g, Severity: gaps.Total(m, nil), Plan: plan.Recommendations,
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
		s.tick(ctx, time.Now())
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
	now := time.Now()
	held := map[string]bool{}
	if s.opt.Refresh != nil {
		rep, err := s.opt.Refresh(ctx, m)
		if err != nil {
			log.Printf("refresh: %v", err)
		}
		st = rep.Sources
		for id, u := range rep.KPIs {
			switch {
			case u.OK:
				s.fresh.Success(id, now)
			case u.Held:
				s.fresh.Held(id)
				held[id] = true
			case u.Warming:
				s.fresh.Warming(id)
			default:
				s.fresh.Failure(id, u.Error, now)
			}
		}
	}
	s.mu.Lock()
	s.model, s.refreshed = m, now
	if s.opt.Refresh != nil {
		s.sources = st
	}
	s.mu.Unlock()
	s.opt.History.Record(m, now, held)
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
	read := func(h http.HandlerFunc) http.Handler { return s.opt.Auth.Require(h, auth.Readers...) }
	propose := func(h http.HandlerFunc) http.Handler { return s.opt.Auth.Require(h, auth.Proposers...) }
	approve := func(h http.HandlerFunc) http.Handler { return s.opt.Auth.Require(h, auth.Approvers...) }

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/meta", s.handleMeta)
	s.opt.Auth.Routes(mux)

	mux.Handle("GET /api/v1/graph", read(s.handleGraph))
	mux.Handle("GET /api/v1/gaps", read(s.handleGaps))
	mux.Handle("GET /api/v1/plan", read(s.handlePlan))
	mux.Handle("POST /api/v1/simulate", read(s.handleSimulate))
	mux.Handle("GET /api/v1/sources", read(s.handleSources))
	mux.Handle("GET /api/v1/freshness", read(s.handleFreshness))
	mux.Handle("GET /api/v1/events", read(s.handleEvents))
	mux.Handle("GET /api/v1/kpis/{id}/history", read(s.handleKPIHistory))
	mux.Handle("POST /api/v1/kpis/{id}/value", propose(s.handleManualValue))
	mux.Handle("GET /api/v1/inputs", read(s.handleInputs))
	mux.Handle("POST /api/v1/ingest/{name}", s.opt.Auth.Require(http.HandlerFunc(s.handleIngest), auth.Ingesters...))

	mux.Handle("GET /api/v1/ai/status", read(s.handleAIStatus))
	mux.Handle("GET /api/v1/ai/digest", read(s.handleAIDigest))
	mux.Handle("GET /api/v1/ai/insights", read(s.handleAIInsights))
	mux.Handle("POST /api/v1/ai/ask", read(s.handleAIAsk))
	mux.Handle("POST /api/v1/ai/explain", read(s.handleAIExplain))
	mux.Handle("POST /api/v1/ai/pack-draft", propose(http.HandlerFunc(s.handlePackDraft)))
	mux.Handle("GET /api/v1/ai/edges", read(s.handleEdges))
	mux.Handle("GET /api/v1/ai/contradictions", read(s.handleContradictions))
	mux.Handle("GET /api/v1/similar", read(s.handleSimilar))
	mux.Handle("GET /api/v1/proposals/{id}/explanation", read(s.handleExplanation))
	mux.Handle("GET /api/v1/proposals/{id}/similar", read(s.handleProposalSimilar))

	mux.Handle("GET /api/v1/proposals", read(s.handleProposals))
	mux.Handle("POST /api/v1/proposals", propose(s.handlePropose))
	mux.Handle("GET /api/v1/proposals/{id}", read(s.handleProposal))
	mux.Handle("POST /api/v1/proposals/{id}/approve", approve(s.handleApprove))
	mux.Handle("POST /api/v1/proposals/{id}/reject", approve(s.handleReject))
	mux.Handle("GET /api/v1/audit", read(s.handleAudit))
	mux.Handle("GET /api/v1/audit/verify", read(s.handleAuditVerify))
	mux.Handle("GET /api/v1/decisions", read(s.handleDecisions))
	mux.Handle("GET /api/v1/decisions/{id}", read(s.handleDecision))
	mux.Handle("GET /api/v1/decisions/{id}/export", read(s.handleDecisionExport))
	mux.Handle("GET /api/v1/policy", read(s.handlePolicy))

	mux.Handle("GET /api/v1/ontology/schema", read(s.handleOntSchema))
	mux.Handle("GET /api/v1/ontology/objects", read(s.handleOntObjects))
	mux.Handle("GET /api/v1/ontology/objects/{id}", read(s.handleOntObject))
	mux.Handle("GET /api/v1/ontology/objects/{id}/impact", read(s.handleOntImpact))
	mux.Handle("GET /api/v1/ontology/risk", read(s.handleOntRisk))
	mux.Handle("GET /api/v1/ontology/objects/{id}/history", read(s.handleOntHistory))
	mux.Handle("POST /api/v1/ontology/ingest/{tenant}", s.opt.Auth.Require(http.HandlerFunc(s.handleOntIngest), auth.Ingesters...))
	mux.Handle("GET /api/v1/ontology/actions", read(s.handleOntActions))
	mux.Handle("GET /api/v1/ontology/views/{id}", read(s.handleOntView))
	mux.Handle("GET /api/v1/ontology/resolution", read(s.handleOntCandidates))
	mux.Handle("POST /api/v1/ontology/resolution/{id}/accept", approve(s.decideCandidate(true)))
	mux.Handle("POST /api/v1/ontology/resolution/{id}/reject", approve(s.decideCandidate(false)))
	mux.Handle("POST /api/v1/ontology/refresh", propose(s.handleOntRefresh))
	mux.Handle("POST /api/v1/ai/propose", propose(s.handleAIPropose))
	if s.opt.Ontology.Scenarios != nil {
		mux.Handle("GET /api/v1/scenarios", read(s.handleScenarios))
		mux.Handle("POST /api/v1/scenarios", propose(s.handleScenarioCreate))
		mux.Handle("GET /api/v1/scenarios/compare", read(s.handleScenarioCompare))
		mux.Handle("GET /api/v1/scenarios/{id}", read(s.handleScenario))
		mux.Handle("POST /api/v1/scenarios/{id}/run", propose(s.handleScenarioRun))
		mux.Handle("DELETE /api/v1/scenarios/{id}", propose(s.handleScenarioDelete))
	}
	mux.Handle("POST /api/v1/exec/{id}", s.opt.Auth.Require(http.HandlerFunc(s.handleExec), auth.Executors...))

	mux.Handle("GET /api/v1/keep/status", read(s.handleKeepStatus))
	mux.Handle("GET /api/v1/keep/approvals", read(s.handleKeepApprovals))
	mux.Handle("POST /api/v1/keep/approvals/{id}", approve(s.handleKeepDecide))
	mux.Handle("GET /api/v1/keep/receipts", read(s.handleKeepReceipts))
	mux.Handle("GET /api/v1/keep/audit", read(s.handleKeepAudit))

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
	mux.Handle("POST /api/v1/exec/{id}", s.opt.Auth.Require(http.HandlerFunc(s.handleExec), auth.Executors...))
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
		"product": "Zyntra", "version": s.opt.Version, "host": s.opt.Host, "model": m.Name, "pack": m.Pack,
		"auth_required": s.opt.Auth.Required(), "auth_methods": s.opt.Auth.Methods(),
		"sources":       map[string]int{"total": len(src), "healthy": healthy},
		"approval_mode": s.opt.ApprovalMode, "execute_mode": s.opt.Executor.Mode,
		"ai_mode": s.opt.AI.Status().Mode, "ontology": s.opt.Ontology.Store != nil,
	})
}

func (s *Server) handleGraph(w http.ResponseWriter, _ *http.Request) {
	m, at := s.snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"model": m, "refreshed_at": at, "version": m.Version(),
		"constraints": m.AllConstraints(), "freshness": s.freshness(m), "owners": m.Owners(),
	})
}

func (s *Server) handleFreshness(w http.ResponseWriter, _ *http.Request) {
	m, at := s.snapshot()
	st := s.freshness(m)
	unusable := freshness.Unusable(st)
	if unusable == nil {
		unusable = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"kpis": st, "unusable": unusable, "refreshed_at": at})
}

func (s *Server) handleGaps(w http.ResponseWriter, r *http.Request) {
	m, _ := s.snapshot()
	g := gaps.ForOwner(gaps.Detect(m), r.URL.Query().Get("owner"))
	if g == nil {
		g = []gaps.Gap{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"gaps": g, "severity_total": gaps.Total(m, nil), "owners": m.Owners()})
}

// plan ranks actions for m using the current freshness.
func (s *Server) plan(m *graph.Model) (planner.Result, []freshness.State, error) {
	st := s.freshness(m)
	r, err := planner.PlanWith(m, planner.Options{Unusable: freshness.Set(st)})
	return r, st, err
}

func (s *Server) handlePlan(w http.ResponseWriter, req *http.Request) {
	m, _ := s.snapshot()
	r, st, err := s.plan(m)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	r = planner.ForOwner(m, r, req.URL.Query().Get("owner"))
	unusable := freshness.Unusable(st)
	if unusable == nil {
		unusable = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recommendations": r.Recommendations, "blocked": r.Blocked,
		"unusable_inputs": unusable, "model_version": m.Version(), "owners": m.Owners(),
	})
}

type simulateRequest struct {
	Action  string        `json:"action"`
	Actions []string      `json:"actions,omitempty"`
	Custom  *graph.Action `json:"custom,omitempty"`
}

// resolveActions looks up action ids, which may be joined with "+".
func resolveActions(m *graph.Model, ids ...string) ([]graph.Action, error) {
	var out []graph.Action
	for _, id := range ids {
		for part := range strings.SplitSeq(id, "+") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			a, ok := m.Action(part)
			if !ok {
				return nil, fmt.Errorf("unknown action %q", part)
			}
			out = append(out, *a)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no actions given")
	}
	return out, nil
}

func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var req simulateRequest
	if !decode(w, r, &req) {
		return
	}
	m, _ := s.snapshot()
	opt := sim.Options{Unusable: freshness.Set(s.freshness(m))}
	var (
		res sim.Result
		err error
	)
	switch {
	case req.Custom != nil:
		res, err = sim.ApplyPlan(m, []graph.Action{*req.Custom}, opt)
	case req.Action != "" || len(req.Actions) > 0:
		var acts []graph.Action
		if acts, err = resolveActions(m, append([]string{req.Action}, req.Actions...)...); err == nil {
			res, err = sim.ApplyPlan(m, acts, opt)
		}
	default:
		err = fmt.Errorf("provide action, actions or custom")
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
	owner, window := r.URL.Query().Get("owner"), r.URL.Query().Get("window")
	snap := s.aiSnapshot(owner)
	if window == "" {
		writeJSON(w, http.StatusOK, s.opt.AI.Digest(r.Context(), snap))
		return
	}
	f, ok := s.shiftFacts(snap.Model, owner, window, time.Now())
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown window "+window)
		return
	}
	a := s.opt.AI.Shift(r.Context(), snap, f)
	writeJSON(w, http.StatusOK, map[string]any{"text": a.Text, "intent": a.Intent, "grounding": a.Grounding, "mode": a.Mode,
		"model": a.Model, "llm_error": a.LLMError, "facts": f})
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
	snap := s.aiSnapshot()
	snap.Objects = s.objectContext(r, snap.Model)
	writeJSON(w, http.StatusOK, s.opt.AI.Ask(r.Context(), q, snap))
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

// mirror copies a local decision into Keep's audit chain in the background.
func (s *Server) mirror(id string) {
	if s.opt.Keep == nil {
		return
	}
	s.bg.Go(func() {
		s.mirrorNow(id)
	})
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
