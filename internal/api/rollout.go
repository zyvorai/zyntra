// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/rollout"
)

// The rollout contract with deployment tooling:
//
//   GET  /api/v1/rollouts            open and finished rollouts
//   GET  /api/v1/rollouts/{id}       one rollout: stages, sites, gates and the
//                                    stage that may run now ("current")
//   POST /api/v1/rollouts/{id}/report  {stage, site, state: started|healthy|failed, note}
//                                    from the tool, with ZYNTRA_DEPLOY_TOKEN
//
// A rollout opens when the decision reaches its final approval. Zyntra never
// deploys. A stage may start only when the previous one has every site healthy
// and its KPI health gates hold on fresh data; a failure or a missed gate
// halts the rollout until a site is reported healthy again, the KPI recovers
// (recheck) or an approver aborts it.

// gateValue reads a KPI for a health gate and says whether it can be trusted.
func (s *Server) gateValue() rollout.GateValue {
	m, _ := s.snapshot()
	unusable := freshness.Set(s.freshness(m))
	return func(kpi string) (float64, bool, bool) {
		k, ok := m.KPI(kpi)
		if !ok {
			return 0, false, false
		}
		return k.Value, !unusable[kpi], true
	}
}

// openRollout creates the rollout for a finally approved decision that
// carries a rollout plan.
func (s *Server) openRollout(p approvals.Proposal, by string) {
	if p.Rollout == nil || len(p.Rollout.Stages) == 0 {
		return
	}
	if _, created, err := s.opt.Rollouts.Create(p.ID, p.Action, p.Tenant, p.Rollout); err == nil && created {
		names := make([]string, len(p.Rollout.Stages))
		for i, st := range p.Rollout.Stages {
			names[i] = st.Name
		}
		_, _ = s.opt.Store.Record(p.ID, by, "rollout opened: "+strings.Join(names, " → "), func(*approvals.Proposal) {})
	}
}

func (s *Server) handleRollouts(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"rollouts": s.opt.Rollouts.List(), "execute_mode": s.opt.Executor.Mode})
}

func (s *Server) handleRollout(w http.ResponseWriter, r *http.Request) {
	ro, ok := s.opt.Rollouts.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "no such rollout")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollout": ro, "execute_mode": s.opt.Executor.Mode})
}

func rolloutStatus(err error) int {
	switch {
	case errors.Is(err, rollout.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, rollout.ErrBadReport):
		return http.StatusBadRequest
	}
	return http.StatusConflict // not the current stage, or finished
}

func (s *Server) handleRolloutReport(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Stage string `json:"stage"`
		Site  string `json:"site"`
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	by := auth.FromContext(r.Context()).Subject
	ro, err := s.opt.Rollouts.Report(id, req.Stage, req.Site, req.State, req.Note, by, s.gateValue())
	if err != nil {
		writeErr(w, rolloutStatus(err), err.Error())
		return
	}
	note := fmt.Sprintf("rollout %s/%s: %s", req.Stage, req.Site, req.State)
	if ro.State != rollout.RolloutOpen {
		note += " → rollout " + ro.State
		if ro.Reason != "" {
			note += " (" + ro.Reason + ")"
		}
	}
	_, _ = s.opt.Store.Record(id, by, note, func(*approvals.Proposal) {})
	writeJSON(w, http.StatusOK, map[string]any{"rollout": ro, "execute_mode": s.opt.Executor.Mode})
}

func (s *Server) handleRolloutRecheck(w http.ResponseWriter, r *http.Request) {
	ro, err := s.opt.Rollouts.Recheck(r.PathValue("id"), s.gateValue())
	if err != nil {
		writeErr(w, rolloutStatus(err), err.Error())
		return
	}
	_, _ = s.opt.Store.Record(ro.ID, auth.FromContext(r.Context()).Subject, "rollout rechecked: "+ro.State, func(*approvals.Proposal) {})
	writeJSON(w, http.StatusOK, map[string]any{"rollout": ro})
}

func (s *Server) handleRolloutAbort(w http.ResponseWriter, r *http.Request) {
	var d decision
	if !decodeOptional(w, r, &d) {
		return
	}
	if strings.TrimSpace(d.Reason) == "" {
		writeErr(w, http.StatusBadRequest, "say why the rollout is being aborted")
		return
	}
	ro, err := s.opt.Rollouts.Abort(r.PathValue("id"), d.Reason)
	if err != nil {
		writeErr(w, rolloutStatus(err), err.Error())
		return
	}
	_, _ = s.opt.Store.Record(ro.ID, auth.FromContext(r.Context()).Subject, "rollout aborted: "+d.Reason, func(*approvals.Proposal) {})
	writeJSON(w, http.StatusOK, map[string]any{"rollout": ro})
}
