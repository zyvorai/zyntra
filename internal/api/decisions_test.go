// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/decisions"
	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/graph"
	"github.com/zyvorai/zyntra/internal/policy"
)

const guarded = `
name: guarded
kpis:
  - {id: lat, value: 400, target: 300, direction: lower}
  - {id: avail, value: 99.9, target: 99.5, direction: higher, criticality: critical, max: 100}
  - {id: cost, value: 100, direction: lower}
constraints:
  - {kpi: cost, ceiling: 110}
actions:
  - id: prio
    name: Priority
    risk: high
    effects: [{kpi: lat, change: -0.3}]
    execute: {template: gravia.priority, params: {name: zyntra-test, value: "1000"}}
  - id: spend
    name: Spend
    effects: [{kpi: lat, change: -0.3}, {kpi: cost, change: 0.5}]
`

func (f *fixture) as(t *testing.T, method, path, user string, roles []auth.Role, body string, out any) int {
	t.Helper()
	v, _ := f.s.opt.Auth.Mint(time.Hour, auth.Identity{Subject: user, Roles: roles, Method: "password"})
	req, _ := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: v})
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (f *fixture) setKPI(id string, v float64) {
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	k, _ := f.s.model.KPI(id)
	k.Value = v
}

func mustPolicy(t *testing.T, text string) *policy.Policy {
	t.Helper()
	p, err := policy.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var (
	viewer   = []auth.Role{auth.RoleViewer}
	proposer = []auth.Role{auth.RoleProposer}
	approver = []auth.Role{auth.RoleApprover}
)

func TestRolesGateRoutes(t *testing.T) {
	f := setupWith(t, guarded, nil)
	if c := f.as(t, "GET", "/api/v1/plan", "vic", viewer, "", nil); c != 200 {
		t.Fatalf("viewer read: %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/proposals", "vic", viewer, `{"action":"prio"}`, nil); c != 403 {
		t.Fatalf("viewer propose: %d", c)
	}
	var p approvals.Proposal
	if c := f.as(t, "POST", "/api/v1/proposals", "pat", proposer, `{"action":"prio"}`, &p); c != 201 {
		t.Fatalf("proposer propose: %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "pat", proposer, "", nil); c != 403 {
		t.Fatalf("proposer approve: %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/exec/"+p.ID, "vic", viewer, "", nil); c != 403 {
		t.Fatalf("viewer exec: %d", c)
	}
	if p.ModelVersion == "" || p.Inputs == nil || p.Inputs.Values["lat"] != 400 || p.Simulation == nil || p.Policy == nil || len(p.Alternatives) == 0 {
		t.Fatalf("decision record incomplete: %+v", p)
	}
	var blocked map[string]any
	if c := f.as(t, "POST", "/api/v1/proposals", "pat", proposer, `{"action":"spend"}`, &blocked); c != 422 || blocked["blocked_reasons"] == nil {
		t.Fatalf("constraint-breaking proposal: %d %v", c, blocked)
	}
}

func TestQuorumAndTwoPersonRule(t *testing.T) {
	f := setupWith(t, guarded, func(o *Options) {
		o.Policy = mustPolicy(t, `rules: [{match: {risk: [high]}, approvals: 2, distinctFromProposer: true}]`)
	})
	var p approvals.Proposal
	f.as(t, "POST", "/api/v1/proposals", "pat", proposer, `{"action":"prio"}`, &p)
	if p.RequiredApprovals != 2 || p.ExpiresAt == nil {
		t.Fatalf("policy not applied: %+v", p)
	}
	if c := f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "pat", []auth.Role{auth.RoleApprover}, "", nil); c != 403 {
		t.Fatalf("proposer self-approval: %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "ann", approver, `{"reason":"looks right"}`, &p); c != 202 || p.Status != approvals.Pending || len(p.Approvals) != 1 {
		t.Fatalf("first approval: %d %+v", c, p)
	}
	if c := f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "ann", approver, "", nil); c != 403 {
		t.Fatalf("double approval: %d", c)
	}
	if len(f.run.calls) != 0 {
		t.Fatal("executed before quorum")
	}
	if c := f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "bob", approver, "", &p); c != 200 || p.Status != approvals.Executed || p.Phase != approvals.PhaseDryRunValidated {
		t.Fatalf("quorum approval: %d %+v", c, p)
	}
	if p.Revalidation == nil || !p.Revalidation.OK || len(p.Approvals) != 2 || p.Approvals[1].Role != "approver" {
		t.Fatalf("record after execution: %+v", p)
	}
}

func TestRevalidationBlocks(t *testing.T) {
	k := &fakeKeep{}
	f := setupWith(t, guarded, func(o *Options) {
		o.Keep, o.ApprovalMode, o.KeepDoubleApproval = k, ModeKeep, true
	})
	f.s.opt.ExecURL = f.ts.URL
	var p approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &p)
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "k3y", "", &p); c != 202 || p.Status != approvals.Approved {
		t.Fatalf("keep approve: %d %+v", c, p)
	}
	f.setKPI("lat", 320)
	var got approvals.Proposal
	f.do(t, "POST", "/api/v1/exec/"+p.ID, "ex3c", "", nil)
	got, _ = f.s.opt.Store.Get(p.ID)
	if got.Status != approvals.Blocked || len(got.BlockedReasons) != 1 || !strings.Contains(got.BlockedReasons[0], "predicted improvement fell") {
		t.Fatalf("drift: %+v", got)
	}
	if len(f.run.calls) != 0 {
		t.Fatal("blocked proposal ran")
	}

	f.setKPI("lat", 400)
	var q approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &q)
	f.do(t, "POST", "/api/v1/proposals/"+q.ID+"/approve", "k3y", "", nil)
	f.s.mu.Lock()
	f.s.model.Edges = append(f.s.model.Edges, graph.Edge{From: "lat", To: "cost", Weight: 0.1})
	f.s.mu.Unlock()
	if c := f.do(t, "POST", "/api/v1/exec/"+q.ID, "ex3c", "", nil); c != 409 {
		t.Fatalf("exec after model change: %d", c)
	}
	got, _ = f.s.opt.Store.Get(q.ID)
	if got.Status != approvals.Blocked || !strings.Contains(strings.Join(got.BlockedReasons, ";"), "model changed") {
		t.Fatalf("model change: %+v", got)
	}
}

func TestKeepRequiredAndMaintenanceWindow(t *testing.T) {
	f := setupWith(t, guarded, func(o *Options) {
		o.Policy = mustPolicy(t, `
maintenanceWindows:
  never: {start: "00:00", end: "00:00"}
  always: {start: "00:00", end: "24:00"}
rules:
  - {match: {actions: [prio]}, keep: required}
`)
	})
	var p approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &p)
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "k3y", "", nil); c != 409 {
		t.Fatalf("keep required without keep: %d", c)
	}
	if got, _ := f.s.opt.Store.Get(p.ID); got.Status != approvals.Blocked || len(f.run.calls) != 0 {
		t.Fatalf("keep required: %+v", got)
	}

	f.s.opt.Policy = mustPolicy(t, `
maintenanceWindows:
  never: {start: "00:00", end: "00:00"}
  always: {start: "00:00", end: "24:00"}
rules:
  - {match: {actions: [prio]}, maintenanceWindows: [never]}
`)
	var q approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &q)
	f.do(t, "POST", "/api/v1/proposals/"+q.ID+"/approve", "k3y", "", &q)
	if q.Status != approvals.Approved || !q.WaitingForWindow || len(f.run.calls) != 0 {
		t.Fatalf("outside window: %+v", q)
	}
	f.s.opt.Store.Update(q.ID, func(x *approvals.Proposal) { x.Policy.Windows = []string{"always"} })
	f.s.tick(context.Background(), time.Now())
	if got, _ := f.s.opt.Store.Get(q.ID); got.Status != approvals.Executed || got.WaitingForWindow || len(f.run.calls) != 1 {
		t.Fatalf("after window opened: %+v", got)
	}
}

func TestOutcomeRegressionProposesRollback(t *testing.T) {
	f := setupWith(t, guarded, func(o *Options) {
		o.Executor = &executor.Executor{Mode: executor.ModeApply, Run: nil}
	})
	f.s.opt.Executor.Run = f.run.run
	var p approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &p)
	f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "k3y", "", &p)
	if p.Phase != approvals.PhaseObserving || p.Outcome == nil || p.Outcome.Success[0].KPI != "lat" {
		t.Fatalf("not observing: %+v", p)
	}
	now := time.Now()
	f.setKPI("lat", 280)
	f.s.observe(now.Add(time.Minute))
	f.s.observe(now.Add(2 * time.Minute))
	got, _ := f.s.opt.Store.Get(p.ID)
	if got.Phase != approvals.PhaseVerified {
		t.Fatalf("verified: %s %+v", got.Phase, got.Outcome)
	}

	f.setKPI("lat", 400)
	var q approvals.Proposal
	f.do(t, "POST", "/api/v1/proposals", "k3y", `{"action":"prio"}`, &q)
	f.do(t, "POST", "/api/v1/proposals/"+q.ID+"/approve", "k3y", "", &q)
	f.setKPI("avail", 99.0)
	f.s.observe(now.Add(time.Minute))
	got, _ = f.s.opt.Store.Get(q.ID)
	if got.Phase != approvals.PhaseRollbackProposed || got.RollbackID == "" || got.Outcome.State != "regressed" {
		t.Fatalf("regressed: %+v", got)
	}
	rb, _ := f.s.opt.Store.Get(got.RollbackID)
	if rb.RollbackOf != q.ID || !strings.Contains(rb.Render, "kubectl delete gryviapriorities.gryvia.io zyntra-test") {
		t.Fatalf("rollback proposal %+v", rb)
	}
	if c := f.do(t, "POST", "/api/v1/proposals/"+rb.ID+"/approve", "k3y", "", &rb); c != 200 || rb.Status != approvals.Executed {
		t.Fatalf("rollback approve: %d %+v", c, rb)
	}
	last := f.run.calls[len(f.run.calls)-1]
	if last[0] != "delete" {
		t.Fatalf("rollback ran %v", last)
	}
	if got, _ = f.s.opt.Store.Get(q.ID); got.Phase != approvals.PhaseRolledBack {
		t.Fatalf("original after rollback: %s", got.Phase)
	}

	var d struct {
		Decision approvals.Proposal `json:"decision"`
		Audit    []approvals.Event  `json:"audit"`
		Linked   approvals.Proposal `json:"linked"`
	}
	if c := f.do(t, "GET", "/api/v1/decisions/"+q.ID, "k3y", "", &d); c != 200 || len(d.Audit) < 5 || d.Linked.ID != rb.ID {
		t.Fatalf("decision: %d audit=%d linked=%s", c, len(d.Audit), d.Linked.ID)
	}
	var e decisions.Export
	if c := f.do(t, "GET", "/api/v1/decisions/"+q.ID+"/export", "k3y", "", &e); c != 200 {
		t.Fatalf("export: %d", c)
	}
	if err := decisions.Verify(e); err != nil || e.Decision.ID != q.ID || !e.Chain.OK {
		t.Fatalf("export verify: %v %+v", err, e.Chain)
	}
	var v struct {
		Chain approvals.Verification `json:"chain"`
	}
	if c := f.do(t, "GET", "/api/v1/audit/verify", "k3y", "", &v); c != 200 || !v.Chain.OK || v.Chain.Events == 0 {
		t.Fatalf("audit verify: %d %+v", c, v)
	}
}
