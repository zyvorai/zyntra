// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"encoding/json"
	"github.com/zyvorai/zyntra/internal/calibrate"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/draft"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/outcome"
	"github.com/zyvorai/zyntra/internal/pack"
	"github.com/zyvorai/zyntra/internal/planner"
	"github.com/zyvorai/zyntra/internal/review"
	"github.com/zyvorai/zyntra/internal/sim"
)

const maxDraftBody = 4 << 20

// handlePackDraft drafts a pack from samples and an industry line. It
// writes nothing: the files come back for a person to edit and save.
func (s *Server) handlePackDraft(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Industry string `json:"industry"`
		ID       string `json:"id"`
		Samples  []struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"samples"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDraftBody)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body (samples up to 4 MiB in total)")
		return
	}
	req.Industry = strings.TrimSpace(req.Industry)
	if req.Industry == "" || len(req.Industry) > 200 {
		writeErr(w, http.StatusBadRequest, "industry must be 1-200 characters")
		return
	}
	if len(req.Samples) == 0 || len(req.Samples) > 8 {
		writeErr(w, http.StatusBadRequest, "send 1 to 8 samples")
		return
	}
	dr := draft.Request{Industry: req.Industry, ID: req.ID}
	for _, smp := range req.Samples {
		dr.Samples = append(dr.Samples, draft.Sample{Name: smp.Name, Data: []byte(smp.Content)})
	}
	res, err := draft.Draft(r.Context(), s.opt.AI.LLM, dr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if rep, err := draft.Validate(r.Context(), res.Files); err == nil {
		res.Validation = rep
	}
	writeJSON(w, http.StatusOK, res)
}

// explainVerdict stores and audits the explanation of a fresh verdict.
func (s *Server) explainVerdict(p approvals.Proposal) {
	if ex, ok := review.Explain(p); ok {
		_, _ = s.opt.Store.Explain(p.ID, "zyntra", ex)
	}
}

func (s *Server) handleExplanation(w http.ResponseWriter, r *http.Request) {
	p, err := s.opt.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	ex := p.Explanation
	stored := ex != nil
	if ex == nil {
		e, ok := review.Explain(p)
		if !ok {
			writeErr(w, http.StatusConflict, "no outcome verdict yet: an explanation needs an applied change and a finished observation")
			return
		}
		ex = &e
	}
	narr := s.opt.AI.Narrate(r.Context(), "Explain why this decision's prediction landed or missed", ex.Text, ex)
	writeJSON(w, http.StatusOK, map[string]any{
		"explanation": ex, "stored": stored, "verified": review.VerifyHash(*ex),
		"narrative": narr.Text, "mode": narr.Mode, "model": narr.Model, "llm_error": narr.LLMError,
	})
}

func (s *Server) handleProposalSimilar(w http.ResponseWriter, r *http.Request) {
	p, err := s.opt.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, review.Similar(s.opt.Store.List(), p, 3))
}

// handleSimilar finds precedents for an action that has not been proposed
// yet, from its simulation.
func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("action")
	m, _ := s.snapshot()
	acts, err := resolveActions(m, strings.Split(id, "+")...)
	if err != nil || id == "" {
		writeErr(w, http.StatusBadRequest, "unknown action")
		return
	}
	res, err := sim.ApplyPlan(m, acts, sim.Options{})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cur := approvals.Proposal{ID: "-", Action: res.Action, Actions: res.Actions,
		Predicted: approvals.Prediction{KPIs: map[string]float64{}, Closes: res.GapsClosed}}
	for _, k := range res.KPIs {
		if k.Change != 0 {
			cur.Predicted.KPIs[k.KPI] = k.After
		}
	}
	writeJSON(w, http.StatusOK, review.Similar(s.opt.Store.List(), cur, 3))
}

func (s *Server) handleEdges(w http.ResponseWriter, _ *http.Request) {
	m, _ := s.snapshot()
	edges := review.MissingEdges(m, s.opt.History.Snapshot())
	if edges == nil {
		edges = []review.EdgeProposal{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"edges": edges,
		"note": "Proposed from KPI history; none of these are in the model. Add one to kpis.yaml only after checking it."})
}

func (s *Server) handleContradictions(w http.ResponseWriter, r *http.Request) {
	m, _ := s.snapshot()
	readme := ""
	if m.Dir != "" {
		if b, err := os.ReadFile(filepath.Join(m.Dir, pack.Readme)); err == nil {
			readme = string(b)
		}
	}
	plan, _, _ := s.plan(m)
	rules := review.Rules(readme, m)
	found := review.Check(m, plan.Recommendations, rules)
	resp := map[string]any{"rules": len(rules), "mode": "heuristic"}
	if open := review.Unchecked(rules); len(open) > 0 && s.opt.AI.LLM != nil {
		resp["mode"] = "llm"
		readings, err := s.opt.AI.ReadRules(r.Context(), open, approvableActions(m, plan.Recommendations))
		if err != nil {
			resp["llm_error"] = err.Error()
		}
		byLine := map[int]review.Rule{}
		for _, ru := range open {
			byLine[ru.Line] = ru
		}
		scored := approvableIDs(plan.Recommendations)
		for _, rd := range readings {
			ru, ok := byLine[rd.Line]
			if ok && scored[rd.Action] && strings.TrimSpace(rd.Why) != "" {
				found = append(found, review.Contradiction{Action: rd.Action, Rule: ru, Why: rd.Why, By: "model"})
			}
		}
	}
	if found == nil {
		found = []review.Contradiction{}
	}
	resp["contradictions"] = found
	writeJSON(w, http.StatusOK, resp)
}

func approvableIDs(recs []planner.Recommendation) map[string]bool {
	out := map[string]bool{}
	for _, r := range recs {
		if r.Status == planner.StatusPendingApproval {
			for _, id := range strings.Split(r.Action, "+") {
				out[id] = true
			}
		}
	}
	return out
}

func approvableActions(m *graph.Model, recs []planner.Recommendation) []map[string]any {
	var out []map[string]any
	for id := range approvableIDs(recs) {
		a, ok := m.Action(id)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"id": a.ID, "name": a.Name, "kind": a.Kind(), "window": a.Window,
			"approvers": a.Approvers, "preconditions": a.Preconditions, "compensate": a.Compensate})
	}
	slices.SortFunc(out, func(a, b map[string]any) int { return strings.Compare(a["id"].(string), b["id"].(string)) })
	return out
}

// shiftFacts gathers the close-of-window facts for an owner.
func (s *Server) shiftFacts(m *graph.Model, owner, window string, now time.Time) (ai.ShiftFacts, bool) {
	win, ok := m.Calendars[window]
	if !ok {
		return ai.ShiftFacts{}, false
	}
	f := ai.ShiftFacts{Owner: owner, Window: window, Open: win.Contains(now)}
	next := nextEdge(win, now)
	if !next.IsZero() {
		f.Next = &next
	}
	reopen := next
	if f.Open && !next.IsZero() {
		reopen = nextEdge(win, next.Add(time.Minute))
	}
	owned := func(id string) bool {
		k, ok := m.KPI(id)
		return ok && (owner == "" || k.Owner == owner)
	}
	var latest *approvals.Proposal
	for _, p := range s.opt.Store.List() {
		if p.Explanation == nil || p.Outcome == nil || p.Outcome.DecidedAt == nil {
			continue
		}
		if latest == nil || p.Outcome.DecidedAt.After(*latest.Outcome.DecidedAt) {
			cp := p
			latest = &cp
		}
	}
	if latest != nil {
		for _, fd := range latest.Explanation.Findings {
			if fd.Kind != "hit" && (fd.KPI == "" || owned(fd.KPI)) {
				f.LastMiss, f.LastMissProposal = fd.Text, latest.ID
				break
			}
		}
	}
	if !reopen.IsZero() {
		for _, st := range s.freshness(m) {
			if !owned(st.KPI) || st.LastSuccess == nil || st.MaxAge == "" || st.Status == freshness.Static {
				continue
			}
			d, err := time.ParseDuration(st.MaxAge)
			if err != nil {
				continue
			}
			if at := st.LastSuccess.Add(d); at.Before(reopen) {
				f.StaleBefore = append(f.StaleBefore, ai.StaleAt{KPI: st.KPI, At: at})
			}
		}
	}
	return f, true
}

// nextEdge is the next time after now that the window opens (when closed)
// or closes (when open), searched in 5-minute steps over eight days.
func nextEdge(w interface{ Contains(time.Time) bool }, now time.Time) time.Time {
	in := w.Contains(now)
	t := now.Truncate(5 * time.Minute)
	for i := 0; i < 8*24*12; i++ {
		t = t.Add(5 * time.Minute)
		if w.Contains(t) != in {
			return t
		}
	}
	return time.Time{}
}

// verdictReady reports whether a proposal just reached an outcome verdict.
func verdictReady(p approvals.Proposal) bool {
	return p.Outcome != nil && p.Outcome.State != outcome.Observing
}

// handleCalibration backtests the model's edge weights against decisions that
// ran and finished, and suggests corrections. It changes nothing.
func (s *Server) handleCalibration(w http.ResponseWriter, _ *http.Request) {
	m, _ := s.snapshot()
	writeJSON(w, http.StatusOK, calibrate.Analyze(m, s.opt.Store.List(), calibrate.Options{}))
}
