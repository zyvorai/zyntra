// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/freshness"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/outcome"
	"github.com/zyvorai/zyntra/internal/policy"
	"github.com/zyvorai/zyntra/internal/sim"
)

var riskRank = map[graph.Risk]int{graph.RiskLow: 0, "": 0, graph.RiskMedium: 1, graph.RiskHigh: 2}

// inputs captures the evidence a decision is made on.
func (s *Server) inputs(m *graph.Model, st []freshness.State) *approvals.Inputs {
	in := &approvals.Inputs{At: time.Now().UTC(), Values: map[string]float64{}, Freshness: st, Sources: s.sourceStatus()}
	for _, k := range m.KPIs {
		in.Values[k.ID] = k.Value
	}
	return in
}

// render renders every executable action; advisory actions are skipped.
func render(m *graph.Model, acts []graph.Action) (templates, kinds []string, display string, err error) {
	var parts []string
	for _, a := range acts {
		if a.Kind() == "" {
			continue
		}
		templates = append(templates, executor.Template(a))
		kinds = append(kinds, a.Kind())
		rd, rerr := executor.RenderIn(m, a)
		if rerr != nil {
			return templates, kinds, "", fmt.Errorf("%s: %w", a.ID, rerr)
		}
		parts = append(parts, rd.Display)
	}
	return templates, kinds, strings.Join(parts, "---\n"), nil
}

func compensations(acts []graph.Action) []string {
	var out []string
	for _, a := range acts {
		if a.Compensate != "" {
			out = append(out, a.Compensate)
		}
	}
	return out
}

func describe(acts []graph.Action) (risk graph.Risk, adapter string) {
	for i, a := range acts {
		if riskRank[a.Risk] > riskRank[risk] {
			risk = a.Risk
		}
		switch {
		case i == 0:
			adapter = a.Adapter
		case adapter != a.Adapter:
			adapter = "multiple"
		}
	}
	return risk, adapter
}

func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action  string   `json:"action"`
		Actions []string `json:"actions"`
	}
	if !decode(w, r, &req) {
		return
	}
	m, _ := s.snapshot()
	acts, err := resolveActions(m, append([]string{req.Action}, req.Actions...)...)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unknown action: "+err.Error())
		return
	}
	st := s.freshness(m)
	res, err := sim.ApplyPlan(m, acts, sim.Options{Unusable: freshness.Set(st)})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(res.Violations) > 0 {
		var why []string
		for _, v := range res.Violations {
			why = append(why, v.Text)
		}
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "blocked by constraints or invariants", "blocked_reasons": why})
		return
	}
	if len(res.PreconditionFailures) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "preconditions not met", "blocked_reasons": res.PreconditionFailures})
		return
	}
	eff := s.opt.Policy.For(acts...)
	risk, adapter := describe(acts)
	ids := make([]string, len(acts))
	for i, a := range acts {
		ids[i] = a.ID
	}
	p := approvals.Proposal{
		Action: res.Action, Actions: ids, ActionName: res.ActionName, Risk: string(risk), Adapter: adapter,
		ModelVersion: m.Version(), Inputs: s.inputs(m, st), Simulation: &res, Policy: &eff,
		RequiredApprovals: eff.Approvals, Compensate: compensations(acts),
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
	if m.Pack != nil {
		p.PackID = m.Pack.ID
	}
	tpls, kinds, display, rerr := render(m, acts)
	p.Template, p.Kinds = strings.Join(tpls, "+"), kinds
	if rerr != nil {
		p.RenderErr = rerr.Error()
	} else {
		p.Render = display
	}
	if plan, _, err := s.plan(m); err == nil {
		for _, rec := range plan.Recommendations {
			if rec.Action != p.Action && len(p.Alternatives) < 3 {
				p.Alternatives = append(p.Alternatives, approvals.Alternative{Action: rec.Action, Name: rec.Name,
					Score: rec.Score, WeightedImprovement: rec.WeightedImprovement, Confidence: rec.Confidence})
			}
		}
		for _, rec := range plan.Blocked {
			if rec.Action != p.Action && len(p.Alternatives) < 5 {
				p.Alternatives = append(p.Alternatives, approvals.Alternative{Action: rec.Action, Name: rec.Name,
					Score: rec.Score, WeightedImprovement: rec.WeightedImprovement, BlockedReasons: rec.BlockedReasons})
			}
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
	writeJSON(w, http.StatusOK, map[string]any{"events": ev, "chain": s.opt.Store.Verify()})
}

func (s *Server) handleAuditVerify(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"chain": s.opt.Store.Verify(), "signing_key": s.opt.Signer.PublicKey()})
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

func keepMode(p approvals.Proposal) string {
	if p.Policy == nil || p.Policy.Keep == "" {
		return policy.KeepPreferred
	}
	return p.Policy.Keep
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
	who := auth.FromContext(r.Context())
	p, done, err := s.opt.Store.Approve(id, approvals.Approval{By: who.Subject, Role: string(who.Role), Method: who.Method, Reason: d.Reason})
	if err != nil {
		code := statusFor(err)
		if errors.Is(err, approvals.ErrForbidden) {
			code = http.StatusForbidden
		}
		writeErr(w, code, err.Error())
		return
	}
	if !done {
		writeJSON(w, http.StatusAccepted, p)
		return
	}
	if p.Template == "" {
		s.mirror(p.ID)
		writeJSON(w, http.StatusOK, p)
		return
	}
	keepOn := s.opt.ApprovalMode == ModeKeep && s.opt.Keep != nil
	switch km := keepMode(p); {
	case km == policy.KeepRequired && !keepOn:
		p = s.block(p.ID, "zyntra", "policy requires Fabric Keep, but Keep approval mode is not enabled")
		writeJSON(w, http.StatusConflict, p)
		return
	case keepOn && km != policy.KeepOff:
		ref, err := s.opt.Keep.Start(r.Context(), p, strings.TrimRight(s.opt.ExecURL, "/")+"/api/v1/exec/"+p.ID)
		if err != nil {
			ref = approvals.KeepRef{Mode: ModeKeep, Error: err.Error()}
			p, _ = s.opt.Store.Update(p.ID, func(x *approvals.Proposal) { x.Keep = &ref })
			if km == policy.KeepRequired {
				p = s.block(p.ID, "zyntra", "Fabric Keep is required but unavailable: "+err.Error())
				writeJSON(w, http.StatusConflict, p)
				return
			}
			log.Printf("keep start %s: %v; executing locally", p.ID, err)
			p = s.runOrSchedule(r.Context(), p.ID, "zyntra (keep unavailable)")
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
	p = s.runOrSchedule(r.Context(), p.ID, who.Subject)
	s.mirror(p.ID)
	writeJSON(w, http.StatusOK, p)
}

// runOrSchedule executes an approved proposal now, or leaves it approved
// until its maintenance window opens.
func (s *Server) runOrSchedule(ctx context.Context, id, by string) approvals.Proposal {
	p, _ := s.opt.Store.Get(id)
	if p.Policy != nil && len(p.Policy.Windows) > 0 {
		if ok, why := s.opt.Policy.InWindow(p.Policy.Windows, time.Now()); !ok {
			out, _ := s.opt.Store.Record(id, by, "scheduled: "+why, func(x *approvals.Proposal) { x.WaitingForWindow = true })
			return out
		}
	}
	return s.execute(ctx, id, by)
}

// block refuses to run an approved proposal for a reason found outside
// revalidation (for example, Keep being required but unavailable).
func (s *Server) block(id, by, reason string) approvals.Proposal {
	m, _ := s.snapshot()
	out, err := s.opt.Store.Block(id, approvals.Revalidation{At: time.Now().UTC(), Reasons: []string{reason}, ModelVersion: m.Version()}, by)
	if err != nil {
		out, _ = s.opt.Store.Get(id)
	}
	s.mirror(id)
	return out
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
	// Keep never ran the call; the human approval stands, so run it here
	// unless policy insists on Keep.
	if aid == "" && p.Status == approvals.Approved {
		if keepMode(p) == policy.KeepRequired {
			s.block(id, "zyntra", "Fabric Keep is required but did not run the action: "+err.Error())
			return
		}
		s.runOrSchedule(ctx, id, "zyntra (keep unavailable)")
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
	switch p.Status {
	case approvals.Failed:
		code = http.StatusBadGateway
	case approvals.Blocked, approvals.Expired:
		code = http.StatusConflict
	}
	writeJSON(w, code, p)
}

// actionsFor returns the actions a proposal runs: its own, or for a
// rollback proposal the inverse of the original's.
func (s *Server) actionsFor(m *graph.Model, p approvals.Proposal) ([]graph.Action, error) {
	ids := p.Actions
	if len(ids) == 0 {
		ids = []string{p.Action}
	}
	if p.RollbackOf == "" {
		return resolveActions(m, ids...)
	}
	orig, err := s.opt.Store.Get(p.RollbackOf)
	if err != nil {
		return nil, err
	}
	ids = orig.Actions
	if len(ids) == 0 {
		ids = []string{orig.Action}
	}
	src, err := resolveActions(m, ids...)
	if err != nil {
		return nil, err
	}
	var out []graph.Action
	for _, a := range src {
		if a.Compensate != "" {
			if ca, ok := m.Action(a.Compensate); ok {
				out = append(out, *ca)
				continue
			}
		}
		if ra, ok := executor.RollbackAction(a); ok {
			out = append(out, ra)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no rollback is defined for %s", orig.Action)
	}
	return out, nil
}

// revalidate re-checks an approved proposal against the current state.
func (s *Server) revalidate(p approvals.Proposal, m *graph.Model, acts []graph.Action) approvals.Revalidation {
	now := time.Now().UTC()
	rv := approvals.Revalidation{At: now, ModelVersion: m.Version()}
	if p.Policy != nil {
		if ok, why := s.opt.Policy.InWindow(p.Policy.Windows, now); !ok {
			rv.Reasons = append(rv.Reasons, why)
		}
	}
	if p.RollbackOf != "" {
		rv.OK = len(rv.Reasons) == 0
		return rv
	}
	if p.ModelVersion != "" && p.ModelVersion != rv.ModelVersion {
		rv.Reasons = append(rv.Reasons, fmt.Sprintf("model changed since the proposal (%s -> %s)", p.ModelVersion, rv.ModelVersion))
	}
	st := s.freshness(m)
	unusable := freshness.Set(st)
	res, err := sim.ApplyPlan(m, acts, sim.Options{Unusable: unusable})
	if err != nil {
		rv.Reasons = append(rv.Reasons, "cannot re-simulate: "+err.Error())
		return rv
	}
	touched := map[string]bool{}
	for _, k := range res.KPIs {
		if k.Change != 0 {
			touched[k.KPI] = true
		}
	}
	requireAll := p.Policy != nil && p.Policy.RequireFresh
	for _, f := range st {
		if f.Usable() {
			continue
		}
		if f.Required || (requireAll && touched[f.KPI]) {
			rv.StaleInputs = append(rv.StaleInputs, f.KPI)
		}
	}
	sort.Strings(rv.StaleInputs)
	if len(rv.StaleInputs) > 0 {
		rv.Reasons = append(rv.Reasons, "required inputs are stale: "+strings.Join(rv.StaleInputs, ", "))
	}
	for _, v := range res.Violations {
		rv.Reasons = append(rv.Reasons, "would breach constraint: "+v.Text)
	}
	rv.Reasons = append(rv.Reasons, res.PreconditionFailures...)
	rv.WeightedImprovement = res.WeightedImprovement()
	orig := p.Predicted.SeverityBefore - p.Predicted.SeverityAfter
	if p.Simulation != nil {
		orig = p.Simulation.WeightedImprovement()
	}
	if orig > 0 {
		rv.Drift = (orig - rv.WeightedImprovement) / orig
		if limit := s.opt.Policy.MaxDrift(); rv.Drift > limit {
			rv.Reasons = append(rv.Reasons, fmt.Sprintf("predicted improvement fell %.0f%% since approval (%.3f -> %.3f), above the %.0f%% limit",
				rv.Drift*100, orig, rv.WeightedImprovement, limit*100))
		}
	}
	rv.OK = len(rv.Reasons) == 0
	return rv
}

// execute revalidates and runs an approved proposal, then opens the outcome
// observation when the change was applied for real.
func (s *Server) execute(ctx context.Context, id, by string) approvals.Proposal {
	p, err := s.opt.Store.Begin(id)
	if err != nil {
		out, _ := s.opt.Store.Get(id)
		return out
	}
	m, _ := s.snapshot()
	acts, aerr := s.actionsFor(m, p)
	if aerr != nil {
		return s.block(id, by, aerr.Error())
	}
	rv := s.revalidate(p, m, acts)
	if !rv.OK {
		out, err := s.opt.Store.Block(id, rv, by)
		if err != nil {
			out, _ = s.opt.Store.Get(id)
		}
		return out
	}
	_, _ = s.opt.Store.Update(id, func(x *approvals.Proposal) { x.Revalidation, x.WaitingForWindow = &rv, false })
	baseline := map[string]float64{}
	for _, k := range m.KPIs {
		baseline[k.ID] = k.Value
	}

	res := executor.Result{Mode: s.opt.Executor.Mode, OK: true}
	if res.Mode == "" {
		res.Mode = executor.ModeDryRun
	}
	var outputs []string
	for _, a := range acts {
		if a.Kind() == "" {
			continue
		}
		rd, err := executor.RenderIn(m, a)
		if err != nil {
			res.OK, res.Error = false, a.ID+": "+err.Error()
			break
		}
		rd.Key = id
		one := s.opt.Executor.Execute(ctx, rd)
		res.Mode = one.Mode
		if res.Kind == "" {
			res.Kind = one.Kind
		} else if one.Kind != res.Kind {
			res.Kind = "multiple"
		}
		if one.Status != 0 {
			res.Status, res.ResponseHash = one.Status, one.ResponseHash
		}
		if one.Written != "" {
			res.Written = one.Written
		}
		if len(res.Args) > 0 {
			res.Args = append(res.Args, ";")
		}
		res.Args = append(res.Args, one.Args...)
		if one.Output != "" {
			outputs = append(outputs, one.Output)
		}
		if !one.OK {
			res.OK, res.Error = false, a.ID+": "+one.Error
			break
		}
	}
	res.Output = strings.Join(outputs, "\n---\n")
	out, err := s.opt.Store.Complete(id, res, by)
	if err != nil {
		out, _ = s.opt.Store.Get(id)
		return out
	}
	if !res.OK || res.Mode != executor.ModeApply {
		return out
	}
	s.RefreshOnce(ctx)
	m, _ = s.snapshot()
	actual := map[string]float64{}
	for kid := range out.Predicted.KPIs {
		if k, ok := m.KPI(kid); ok {
			actual[kid] = k.Value
		}
	}
	_ = s.opt.Store.RecordActual(id, actual)
	if p.RollbackOf != "" {
		_, _ = s.opt.Store.Record(p.RollbackOf, by, "rolled back by "+id, func(x *approvals.Proposal) {
			x.Phase = approvals.PhaseRolledBack
		})
		out, _ = s.opt.Store.Get(id)
		return out
	}
	spec := outcome.SpecFor(m, acts, baseline, out.Predicted.KPIs)
	rec := outcome.Start(spec, baseline, time.Now().UTC())
	out, _ = s.opt.Store.Record(id, "zyntra", fmt.Sprintf("observing outcome for %s", rec.Window), func(x *approvals.Proposal) {
		x.Outcome, x.Phase = rec, approvals.PhaseObserving
	})
	return out
}

// tick runs the periodic decision work: expiring stale approvals, running
// approvals whose maintenance window opened, and observing outcomes.
func (s *Server) tick(ctx context.Context, now time.Time) {
	ids, _ := s.opt.Store.ExpireDue()
	for _, id := range ids {
		s.mirror(id)
	}
	for _, p := range s.opt.Store.List() {
		if p.Status == approvals.Approved && p.WaitingForWindow && p.Policy != nil {
			if ok, _ := s.opt.Policy.InWindow(p.Policy.Windows, now); ok {
				s.execute(ctx, p.ID, "zyntra (maintenance window)")
				s.mirror(p.ID)
			}
		}
	}
	s.observe(now)
}

// observe samples every open outcome observation.
func (s *Server) observe(now time.Time) {
	ids := s.opt.Store.Observing()
	if len(ids) == 0 {
		return
	}
	m, _ := s.snapshot()
	unusable := freshness.Set(s.freshness(m))
	for _, id := range ids {
		var verdict bool
		p, err := s.opt.Store.Update(id, func(x *approvals.Proposal) {
			if verdict = x.Outcome.Observe(m, unusable, now); verdict {
				x.Phase = approvals.Phase(x.Outcome.State)
			}
		})
		if err != nil || !verdict {
			continue
		}
		p, _ = s.opt.Store.Record(id, "zyntra", fmt.Sprintf("outcome %s: %s", p.Outcome.State, strings.Join(p.Outcome.Reasons, "; ")), func(*approvals.Proposal) {})
		if p.Outcome.State == outcome.Regressed {
			s.proposeRollback(m, p)
		}
		s.mirror(id)
	}
}

// proposeRollback opens a linked proposal that undoes a regressed decision.
func (s *Server) proposeRollback(m *graph.Model, p approvals.Proposal) {
	rb := approvals.Proposal{RollbackOf: p.ID}
	acts, err := s.actionsFor(m, rb)
	if err != nil {
		_, _ = s.opt.Store.Record(p.ID, "zyntra", "no rollback available: "+err.Error(), func(*approvals.Proposal) {})
		return
	}
	tpls, kinds, display, rerr := render(m, acts)
	ids := make([]string, len(acts))
	for i, a := range acts {
		ids[i] = a.ID
	}
	rb = approvals.Proposal{
		Action: p.Action + ".rollback", Actions: ids, ActionName: "Roll back: " + p.ActionName, PackID: p.PackID,
		Risk: p.Risk, Adapter: p.Adapter, Template: strings.Join(tpls, "+"), Kinds: kinds, Render: display,
		ModelVersion: m.Version(), Inputs: s.inputs(m, s.freshness(m)), Policy: p.Policy,
		RequiredApprovals: p.RequiredApprovals, RollbackOf: p.ID, Baseline: map[string]float64{},
		Predicted: approvals.Prediction{KPIs: map[string]float64{}},
	}
	if rerr != nil {
		rb.RenderErr = rerr.Error()
	}
	if rb.Policy != nil {
		pol := *rb.Policy
		pol.DistinctFromProposer = false
		rb.Policy = &pol
	}
	out, _, err := s.opt.Store.Create(rb, "zyntra")
	if err != nil {
		log.Printf("rollback for %s: %v", p.ID, err)
		return
	}
	_, _ = s.opt.Store.Record(p.ID, "zyntra", "rollback proposed: "+out.ID, func(x *approvals.Proposal) {
		x.RollbackID, x.Phase = out.ID, approvals.PhaseRollbackProposed
	})
}

func (s *Server) handleDecisions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"decisions": s.opt.Store.List(), "chain": s.opt.Store.Verify()})
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	p, err := s.opt.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	out := map[string]any{"decision": p, "audit": s.opt.Store.AuditFor(p.ID)}
	for _, link := range []string{p.RollbackID, p.RollbackOf} {
		if link == "" {
			continue
		}
		if x, err := s.opt.Store.Get(link); err == nil {
			out["linked"] = x
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDecisionExport(w http.ResponseWriter, r *http.Request) {
	p, err := s.opt.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	e, err := s.opt.Signer.Sign(p, s.opt.Store.AuditFor(p.ID), s.opt.Store.Verify(), auth.FromContext(r.Context()).Subject, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="zyntra-decision-%s.json"`, p.ID))
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handlePolicy(w http.ResponseWriter, _ *http.Request) {
	m, _ := s.snapshot()
	per := map[string]policy.Effective{}
	for _, a := range m.Actions {
		per[a.ID] = s.opt.Policy.For(a)
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": s.opt.Policy, "max_drift": s.opt.Policy.MaxDrift(), "actions": per})
}
