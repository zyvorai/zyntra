// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/actions"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/calibrate"
	"github.com/zyvorai/zyntra/internal/connector"
	"github.com/zyvorai/zyntra/internal/decisions"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/rollout"
	"github.com/zyvorai/zyntra/internal/scenario"
)

const ontModel = `
name: ont
kpis:
  - {id: queue, value: 20, target: 10, direction: lower, owner: ops}
  - {id: lat, value: 100, target: 200, direction: lower, owner: ops}
edges:
  - {from: queue, to: lat, weight: 0.1}
actions:
  - id: add_gpus
    name: Add GPUs
    effects: [{kpi: queue, change: -0.6}]
    execute: {template: gravia.priority, params: {name: zyntra-test, value: "1000"}}
  - id: free_action
    name: Free action
    effects: [{kpi: queue, change: -0.1}]
`

const ontDef = `
objects:
  - name: Cluster
    kpis: [queue]
    properties: [{name: name, type: string}, {name: status, type: string}]
  - name: Customer
    properties: [{name: name, type: string}, {name: contact, type: string, sensitive: true}]
links:
  - {name: serves, from: Customer, to: Cluster}
actions:
  - id: add_gpus
    inputs: [{name: cluster, object_type: Cluster, required: true}]
    requires: [{input: cluster, property: status, equals: active}]
    permissions: [approver]
    outcome: [{object_type: Cluster, must_be_safe: true}]
rollout:
  stages: [{name: canary, sites: [a]}]
`

func ontSetup(t *testing.T, rules []ontology.Rule) *fixture {
	t.Helper()
	def, err := ontology.ParseDefinition([]byte(ontDef))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", def.Schema())
	now := time.Now()
	_, err = st.Ingest("crm", "t", []ontology.Record{
		{Type: "Cluster", Namespace: "x", Key: "c1", Props: map[string]any{"name": "Cluster One", "status": "active"}},
		{Type: "Cluster", Namespace: "x", Key: "c2", Props: map[string]any{"name": "Cluster Two", "status": "retired"}},
		{Type: "Customer", Namespace: "x", Key: "k1", Props: map[string]any{"name": "Acme", "contact": "a@acme.example"},
			Links: []ontology.RecordLink{{Type: "serves", ToType: "Cluster", ToNS: "x", ToKey: "c1"}}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	ac := &ontology.Access{Schema: def.Schema(), Rules: rules}
	scn, _ := scenario.Open("")
	return setupWith(t, ontModel, func(o *Options) {
		o.Ontology = OntologyOptions{Def: def, Store: st, Access: ac, Actions: actions.New(def, st, ac), Scenarios: scn}
	})
}

func TestOntologyObjectsRespectAccess(t *testing.T) {
	f := ontSetup(t, []ontology.Rule{{Roles: []string{"viewer"}, Types: []string{"Cluster"}}})
	var list struct{ Objects []ontology.Object }
	if c := f.as(t, "GET", "/api/v1/ontology/objects", "v", viewer, "", &list); c != 200 || len(list.Objects) != 2 {
		t.Fatalf("viewer saw %d objects (%d)", len(list.Objects), c)
	}
	if c := f.as(t, "GET", "/api/v1/ontology/objects/Customer:x:k1", "v", viewer, "", nil); c != 404 {
		t.Fatalf("hidden object = %d, want 404", c)
	}
	var adm struct{ Objects []ontology.Object }
	f.as(t, "GET", "/api/v1/ontology/objects?type=Customer", "root", []auth.Role{auth.RoleAdmin}, "", &adm)
	if len(adm.Objects) != 1 || adm.Objects[0].Props["contact"].V != "a@acme.example" {
		t.Fatalf("admin view wrong: %+v", adm.Objects)
	}
	var appr struct{ Objects []ontology.Object }
	f.as(t, "GET", "/api/v1/ontology/objects?type=Customer", "boss", approver, "", &appr)
	if len(appr.Objects) != 1 {
		t.Fatal("approver should see customers (rule only limits viewers)")
	}
	if _, ok := appr.Objects[0].Props["contact"]; ok {
		t.Error("sensitive property leaked to a non-admin")
	}
}

func TestAskCitesObjectsAndHidesForbidden(t *testing.T) {
	f := ontSetup(t, []ontology.Rule{{Roles: []string{"viewer"}, Types: []string{"Cluster"}}})
	var a ai.Answer
	f.as(t, "POST", "/api/v1/ai/ask", "boss", approver, `{"question":"Which customers are at risk and why?"}`, &a)
	if !strings.Contains(a.Text, "Acme") || len(a.Citations) == 0 {
		t.Fatalf("approver answer lacks the customer or citations: %+v", a)
	}
	for _, c := range a.Citations {
		if c.Source == "" || c.ObservedAt.IsZero() {
			t.Errorf("citation without provenance: %+v", c)
		}
	}
	var v ai.Answer
	f.as(t, "POST", "/api/v1/ai/ask", "v", viewer, `{"question":"Which customers are at risk and why?"}`, &v)
	if strings.Contains(v.Text, "Acme") || strings.Contains(v.Text, "a@acme") {
		t.Fatalf("viewer answer leaked a forbidden object: %s", v.Text)
	}
	for _, c := range v.Citations {
		if strings.HasPrefix(c.Object, "Customer:") {
			t.Errorf("viewer citation points at a hidden object: %+v", c)
		}
	}
}

func TestTypedActionContract(t *testing.T) {
	f := ontSetup(t, nil)
	post := func(user string, roles []auth.Role, body string, out any) int {
		return f.as(t, "POST", "/api/v1/proposals", user, roles, body, out)
	}
	if c := post("p", proposer, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:c1"}}`, nil); c != 422 {
		t.Errorf("proposer without the approver role = %d, want 422", c)
	}
	if c := post("boss", approver, `{"action":"add_gpus"}`, nil); c != 422 {
		t.Errorf("missing required input = %d, want 422", c)
	}
	if c := post("boss", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:nope"}}`, nil); c != 422 {
		t.Errorf("unknown object = %d, want 422", c)
	}
	if c := post("boss", approver, `{"action":"add_gpus","inputs":{"cluster":"Customer:x:k1"}}`, nil); c != 422 {
		t.Errorf("wrong object type = %d, want 422", c)
	}
	if c := post("boss", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:c2"}}`, nil); c != 422 {
		t.Errorf("object precondition (status must be active) = %d, want 422", c)
	}
	var p approvals.Proposal
	if c := post("boss", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:c1"}}`, &p); c != 201 {
		t.Fatalf("valid typed proposal = %d", c)
	}
	if len(p.Objects) != 1 || p.Objects[0].ID != "Cluster:x:c1" || p.Rollout == nil || p.ActionInputs["cluster"] != "Cluster:x:c1" {
		t.Fatalf("proposal lacks objects/rollout: %+v", p)
	}
	var ex decisions.Export
	if c := f.as(t, "GET", "/api/v1/decisions/"+p.ID+"/export", "boss", approver, "", &ex); c != 200 || ex.Decision.Rollout == nil || ex.Decision.Rollout.Stages[0].Name != "canary" {
		t.Fatalf("the signed decision must carry the rollout shape: %d %+v", c, ex.Decision.Rollout)
	}
	if err := decisions.Verify(ex); err != nil {
		t.Errorf("export does not verify: %v", err)
	}
	// An untyped action is not affected by the contract.
	if c := post("p", proposer, `{"action":"free_action"}`, nil); c != 201 {
		t.Errorf("untyped action = %d, want 201", c)
	}
}

func TestAIProposeDraftsButCreatesNothing(t *testing.T) {
	f := ontSetup(t, nil)
	var d ai.Draft
	f.as(t, "POST", "/api/v1/ai/propose", "boss", approver, `{"text":"please add gpus to Cluster One"}`, &d)
	if d.Action != "add_gpus" || d.Inputs["cluster"] != "Cluster:x:c1" || !d.Valid {
		t.Fatalf("draft = %+v", d)
	}
	var bad ai.Draft
	f.as(t, "POST", "/api/v1/ai/propose", "boss", approver, `{"text":"add gpus to Cluster Two"}`, &bad)
	if bad.Valid || len(bad.Problems) == 0 {
		t.Fatalf("a retired cluster must not validate: %+v", bad)
	}
	if got := f.s.opt.Store.List(); len(got) != 0 {
		t.Fatalf("drafting created %d proposals", len(got))
	}
}

func TestScenarioRunAndCompare(t *testing.T) {
	f := ontSetup(t, nil)
	var a, b scenario.Scenario
	if c := f.as(t, "POST", "/api/v1/scenarios", "p", proposer, `{"name":"big","actions":["add_gpus"]}`, &a); c != 201 {
		t.Fatalf("create = %d", c)
	}
	f.as(t, "POST", "/api/v1/scenarios", "p", proposer, `{"name":"small","actions":["free_action"],"assumptions":{"queue":30}}`, &b)
	if a.Result == nil || len(a.Result.AtRiskBefore) != 2 || len(a.Result.AtRiskAfter) != 0 {
		t.Fatalf("add_gpus should clear both at-risk clusters (both are bound to the queue KPI): %+v", a.Result)
	}
	if len(a.Result.ExposedBefore) != 1 || a.ModelVersion == "" || a.DataVersion == "" {
		t.Fatalf("missing exposure or version stamps: %+v", a)
	}
	if len(b.Result.AtRiskAfter) != 2 {
		t.Fatalf("small plan with a worse assumption should leave the cluster at risk: %+v", b.Result)
	}
	var cmp scenario.Comparison
	if c := f.as(t, "GET", "/api/v1/scenarios/compare?ids="+a.ID+","+b.ID, "v", viewer, "", &cmp); c != 200 || len(cmp.AtRisk) != 2 || cmp.AtRisk[0] != 0 || cmp.AtRisk[1] != 2 {
		t.Fatalf("compare = %d %+v", c, cmp)
	}
	var again scenario.Scenario
	f.as(t, "POST", "/api/v1/scenarios/"+a.ID+"/run", "p", proposer, "", &again)
	if again.DataVersion != a.DataVersion || again.Result.WeightedAfter != a.Result.WeightedAfter {
		t.Error("re-running the same scenario on the same data changed the result")
	}
	if c := f.as(t, "POST", "/api/v1/scenarios", "v", viewer, `{"name":"x","actions":["add_gpus"]}`, nil); c != 403 {
		t.Errorf("viewer creating a scenario = %d", c)
	}
}

func TestResolutionNeedsApprover(t *testing.T) {
	f := ontSetup(t, nil)
	if c := f.as(t, "POST", "/api/v1/ontology/resolution/x/accept", "v", viewer, "", nil); c != 403 {
		t.Fatalf("viewer accept = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/ontology/resolution/none/accept", "boss", approver, "", nil); c != 409 {
		t.Fatalf("unknown candidate = %d", c)
	}
}

func TestNoOntologyRoutes404(t *testing.T) {
	f := setup(t, nil)
	if c := f.as(t, "GET", "/api/v1/ontology/objects", "v", viewer, "", nil); c != 404 {
		t.Fatalf("pack without ontology = %d, want 404", c)
	}
}

func TestApprovedProposalBlockedWhenObjectsChange(t *testing.T) {
	f := ontSetup(t, nil)
	var p approvals.Proposal
	if c := f.as(t, "POST", "/api/v1/proposals", "boss", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:c1"}}`, &p); c != 201 {
		t.Fatalf("propose = %d", c)
	}
	if p.ObjectDigest == "" {
		t.Fatal("typed proposal carries no object digest")
	}
	// The cluster is retired after the proposal and before the approval.
	_, err := f.s.opt.Ontology.Store.Ingest("ops", "t", []ontology.Record{{Type: "Cluster", Namespace: "x", Key: "c1",
		Props: map[string]any{"status": "retired"}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var done approvals.Proposal
	f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "root", []auth.Role{auth.RoleAdmin}, `{}`, &done)
	if done.Status != approvals.Blocked || done.Execution != nil {
		t.Fatalf("a changed object must block execution: %+v", done)
	}
	if done.Revalidation == nil || !strings.Contains(strings.Join(done.Revalidation.Reasons, " "), "business objects") {
		t.Fatalf("reason missing: %+v", done.Revalidation)
	}
}

func TestPushIngestNeedsGrantAndKeepsTenants(t *testing.T) {
	rules := []ontology.Rule{{Roles: []string{"ingest"}, IngestTenants: []string{"alpha"}}}
	f := ontSetup(t, rules)
	f.s.opt.Auth.SetIngestToken("ingest-tok")
	body := `{"source":"mes","records":[{"type":"Cluster","namespace":"mes","key":"c9","props":{"name":"Pushed","status":"active"}}]}`
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", "ingest-tok", body, nil); c != 202 {
		t.Fatalf("granted tenant = %d", c)
	}
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/beta", "ingest-tok", body, nil); c != 403 {
		t.Fatalf("tenant without a grant = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/ontology/ingest/alpha", "boss", approver, body, nil); c != 403 {
		t.Fatalf("approver is not an ingester = %d", c)
	}
	if c := f.do(t, "GET", "/api/v1/ontology/objects", "ingest-tok", "", nil); c != 403 {
		t.Fatalf("the ingest token must not read objects = %d", c)
	}
	o, ok := f.s.opt.Ontology.Store.Get("Cluster:mes:c9")
	if !ok || o.Tenant != "alpha" || o.Props["name"].Prov.Source != "push:mes" {
		t.Fatalf("pushed object = %+v", o)
	}
	bad := `{"source":"mes","records":[{"type":"Cluster","namespace":"mes","key":"c10","props":{"name":"ok"}},{"type":"Cluster","namespace":"mes","key":"c11","props":{"nope":"x"}}]}`
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", "ingest-tok", bad, nil); c != 422 {
		t.Fatalf("invalid record = %d", c)
	}
	if _, ok := f.s.opt.Ontology.Store.Get("Cluster:mes:c10"); ok {
		t.Error("an invalid batch was partly written")
	}
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", "ingest-tok", `{"source":"mes","records":[]}`, nil); c != 400 {
		t.Fatalf("empty batch = %d", c)
	}
}

func TestObjectHistoryEndpoint(t *testing.T) {
	f := ontSetup(t, nil)
	_, _ = f.s.opt.Ontology.Store.Ingest("ops", "t", []ontology.Record{{Type: "Cluster", Namespace: "x", Key: "c1", Props: map[string]any{"status": "degraded"}}}, time.Now())
	var h struct{ Changes []ontology.Change }
	if c := f.as(t, "GET", "/api/v1/ontology/objects/Cluster:x:c1/history", "boss", approver, "", &h); c != 200 {
		t.Fatalf("history = %d", c)
	}
	last := h.Changes[len(h.Changes)-1]
	if last.Property != "status" || last.Before == nil || last.Before.V != "active" || last.After.V != "degraded" {
		t.Fatalf("last change = %+v", last)
	}
	if c := f.as(t, "GET", "/api/v1/ontology/objects/Cluster:x:none/history", "boss", approver, "", nil); c != 200 {
		t.Fatalf("unknown object history = %d", c)
	}
}

const liveOntDef = `
objects:
  - name: Service
    properties: [{name: name, type: string}, {name: tier, type: string}]
links: []
connectors:
  - name: k8s-services
    kind: kubernetes
    resource: deployments
    interval: 30s
    fields: {name: metadata.name, tier: metadata.labels.tier}
    mapping: {type: Service, namespace: k8s, key: name, props: {name: name, tier: tier}}
actions:
  - id: add_gpus
    inputs: [{name: service, object_type: Service, required: true}]
    requires: [{input: service, property: tier, equals: inference}]
    evidence: [{input: service, properties: [tier], max_age: 10m}]
`

func TestScheduledRefreshUnblocksStaleEvidence(t *testing.T) {
	def, err := ontology.ParseDefinition([]byte(liveOntDef))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", def.Schema())
	// The service was last seen an hour ago.
	hourAgo := time.Now().Add(-time.Hour)
	if _, err := st.Ingest("k8s", "t", []ontology.Record{{Type: "Service", Namespace: "k8s", Key: "infer",
		Props: map[string]any{"name": "infer", "tier": "inference"}, ObservedAt: hourAgo}}, hourAgo); err != nil {
		t.Fatal(err)
	}
	kube := func(context.Context, connector.KubeQuery) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"items":[{"metadata":{"name":"infer","labels":{"tier":"inference"}}}]}`)), nil
	}
	opt := connector.Options{Kubectl: kube}
	sched, err := connector.NewScheduler(st, def, t.TempDir(), nil, opt, "")
	if err != nil {
		t.Fatal(err)
	}
	ac := &ontology.Access{Schema: def.Schema()}
	scn, _ := scenario.Open("")
	f := setupWith(t, ontModel, func(o *Options) {
		o.Ontology = OntologyOptions{Def: def, Store: st, Access: ac, Actions: actions.New(def, st, ac), Scenarios: scn, Scheduler: sched, Connector: opt}
	})
	body := `{"action":"add_gpus","inputs":{"service":"Service:k8s:infer"}}`
	var blocked struct {
		Reasons []string `json:"blocked_reasons"`
	}
	if c := f.as(t, "POST", "/api/v1/proposals", "boss", approver, body, &blocked); c != 422 {
		t.Fatalf("stale evidence = %d, want 422", c)
	}
	// (the body is only decoded on success, so read the reason from the registry)
	if _, problems := f.s.opt.Ontology.Actions.Validate("add_gpus", map[string]string{"service": "Service:k8s:infer"},
		ontology.Principal{Roles: []string{"admin"}}, func(string) bool { return true }); len(problems) != 1 || !strings.Contains(problems[0], "stale") {
		t.Fatalf("want one stale-evidence problem, got %v", problems)
	}

	// Connector status is for approvers and admins, and the run needs an admin.
	if c := f.as(t, "GET", "/api/v1/ontology/connectors", "v", viewer, "", nil); c != 403 {
		t.Errorf("viewer connectors = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/ontology/connectors/k8s-services/run", "boss", approver, "", nil); c != 403 {
		t.Errorf("approver run = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/ontology/connectors/nope/run", "root", []auth.Role{auth.RoleAdmin}, "", nil); c != 404 {
		t.Errorf("unknown connector = %d", c)
	}
	var rep ontology.IngestReport
	if c := f.as(t, "POST", "/api/v1/ontology/connectors/k8s-services/run", "root", []auth.Role{auth.RoleAdmin}, "", &rep); c != 200 || rep.Objects != 1 {
		t.Fatalf("run = %d %+v", c, rep)
	}
	var created approvals.Proposal
	if c := f.as(t, "POST", "/api/v1/proposals", "boss", approver, body, &created); c != 201 {
		t.Fatalf("after the refresh the proposal should be allowed, got %d", c)
	}
	var list struct{ Connectors []connector.Status }
	f.as(t, "GET", "/api/v1/ontology/connectors", "boss", approver, "", &list)
	if len(list.Connectors) != 1 || !list.Connectors[0].Healthy || list.Connectors[0].Runs != 1 || list.Connectors[0].Kind != "kubernetes" {
		t.Fatalf("status = %+v", list.Connectors)
	}
	// A tenant account cannot see or trigger connectors.
	for _, m := range []string{"GET", "POST"} {
		path := "/api/v1/ontology/connectors"
		if m == "POST" {
			path += "/k8s-services/run"
		}
		if c := f.asTenant(t, m, path, "ann", "alpha", approver, "", nil); c != 403 {
			t.Errorf("tenant %s %s = %d", m, path, c)
		}
	}
	// The scheduled refresh endpoint now runs through the scheduler.
	if c := f.as(t, "POST", "/api/v1/ontology/refresh", "p", proposer, "", nil); c != 200 {
		t.Errorf("refresh = %d", c)
	}
}

func TestCalibrationEndpoint(t *testing.T) {
	f := setup(t, nil)
	var rep calibrate.Report
	if c := f.as(t, "GET", "/api/v1/ai/calibration", "v", viewer, "", &rep); c != 200 || rep.Decisions != 0 || !strings.Contains(rep.Note, "dry-run") {
		t.Fatalf("calibration = %d %+v", c, rep)
	}
	if c := f.do(t, "GET", "/api/v1/ai/calibration", "", "", nil); c != 401 {
		t.Errorf("anonymous = %d", c)
	}
}

func TestRolloutContractWithDeploymentTooling(t *testing.T) {
	f := ontSetup(t, nil)
	f.s.opt.Auth.SetDeployToken("deploy-tok")
	max15 := 15.0
	f.s.opt.Ontology.Def.Rollout = &ontology.Rollout{Stages: []ontology.Stage{
		{Name: "canary", Sites: []string{"a"}, Gates: []ontology.Gate{{KPI: "queue", Max: &max15}}},
		{Name: "fleet", Sites: []string{"b"}},
	}}
	var p approvals.Proposal
	if c := f.as(t, "POST", "/api/v1/proposals", "boss", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:c1"}}`, &p); c != 201 {
		t.Fatalf("propose = %d", c)
	}
	var none struct{ Rollouts []rollout.Rollout }
	f.as(t, "GET", "/api/v1/rollouts", "v", viewer, "", &none)
	if len(none.Rollouts) != 0 {
		t.Fatal("a rollout opened before the decision was approved")
	}
	f.as(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "root", []auth.Role{auth.RoleAdmin}, `{}`, nil)

	var one struct{ Rollout rollout.Rollout }
	if c := f.as(t, "GET", "/api/v1/rollouts/"+p.ID, "v", viewer, "", &one); c != 200 || one.Rollout.Current != "canary" || one.Rollout.State != rollout.RolloutOpen {
		t.Fatalf("rollout = %d %+v", c, one.Rollout)
	}
	report := func(token, stage, site, state string, out any) int {
		body := `{"stage":"` + stage + `","site":"` + site + `","state":"` + state + `"}`
		return f.do(t, "POST", "/api/v1/rollouts/"+p.ID+"/report", token, body, out)
	}
	if c := report("deploy-tok", "fleet", "b", "healthy", nil); c != 409 {
		t.Errorf("reporting a later stage = %d, want 409", c)
	}
	// The queue is 20, over the gate's 15: a healthy canary still halts the rollout.
	var halted struct{ Rollout rollout.Rollout }
	if c := report("deploy-tok", "canary", "a", "healthy", &halted); c != 200 || halted.Rollout.State != rollout.RolloutHalted || halted.Rollout.Stages[0].State != rollout.StageBlocked {
		t.Fatalf("gate = %d %+v", c, halted.Rollout)
	}
	if g := halted.Rollout.Stages[0].GateCheck[0]; g.OK || g.Value == nil || *g.Value != 20 {
		t.Errorf("gate check = %+v", g)
	}
	if c := f.as(t, "POST", "/api/v1/rollouts/"+p.ID+"/recheck", "boss", approver, "", &one); c != 200 || one.Rollout.State != rollout.RolloutHalted {
		t.Errorf("recheck before recovery = %d %s", c, one.Rollout.State)
	}
	// The KPI recovers; an approver rechecks and the next stage opens.
	f.s.mu.Lock()
	k, _ := f.s.model.KPI("queue")
	k.Value = 12
	f.s.mu.Unlock()
	if c := f.as(t, "POST", "/api/v1/rollouts/"+p.ID+"/recheck", "boss", approver, "", &one); c != 200 || one.Rollout.Current != "fleet" {
		t.Fatalf("recheck after recovery = %d %+v", c, one.Rollout)
	}
	var done struct{ Rollout rollout.Rollout }
	if c := report("deploy-tok", "fleet", "b", "healthy", &done); c != 200 || done.Rollout.State != rollout.RolloutComplete {
		t.Fatalf("final stage = %d %+v", c, done.Rollout)
	}
	if c := report("deploy-tok", "fleet", "b", "healthy", nil); c != 409 {
		t.Errorf("a finished rollout accepted a report = %d", c)
	}

	// Who may do what.
	if c := f.as(t, "POST", "/api/v1/rollouts/"+p.ID+"/report", "boss", approver, `{"stage":"canary","site":"a","state":"healthy"}`, nil); c != 403 {
		t.Errorf("a human approver reporting as the deploy tool = %d, want 403", c)
	}
	for _, path := range []string{"/api/v1/gaps", "/api/v1/proposals", "/api/v1/ontology/objects", "/api/v1/audit"} {
		if c := f.do(t, "GET", path, "deploy-tok", "", nil); c != 403 {
			t.Errorf("the deploy token read %s = %d, want 403", path, c)
		}
	}
	if c := f.do(t, "GET", "/api/v1/rollouts", "deploy-tok", "", nil); c != 200 {
		t.Errorf("the deploy token should read rollouts: %d", c)
	}
	if c := f.asTenant(t, "GET", "/api/v1/rollouts", "ann", "alpha", approver, "", nil); c != 403 {
		t.Errorf("a tenant read rollouts = %d", c)
	}
	if c := f.as(t, "POST", "/api/v1/rollouts/"+p.ID+"/abort", "boss", approver, `{}`, nil); c != 400 {
		t.Errorf("abort without a reason = %d, want 400", c)
	}
	// Everything the tool reported is in the decision's audit trail.
	var dec struct {
		Audit []approvals.Event `json:"audit"`
	}
	f.as(t, "GET", "/api/v1/decisions/"+p.ID, "root", []auth.Role{auth.RoleAdmin}, "", &dec)
	var notes []string
	for _, e := range dec.Audit {
		notes = append(notes, e.Note)
	}
	joined := strings.Join(notes, "|")
	for _, want := range []string{"rollout opened: canary → fleet", "rollout canary/a: healthy → rollout halted", "rollout fleet/b: healthy → rollout complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit trail lacks %q:\n%s", want, joined)
		}
	}
}

func TestOntologyStatsEndpoint(t *testing.T) {
	f := ontSetup(t, nil)
	f.s.opt.Ontology.Store.SetMaxObjects(500)
	var st ontology.Stats
	if c := f.as(t, "GET", "/api/v1/ontology/stats", "boss", approver, "", &st); c != 200 || st.Objects != 3 || st.Limit != 500 || st.ByType["Cluster"] != 2 {
		t.Fatalf("stats = %d %+v", c, st)
	}
	if c := f.as(t, "GET", "/api/v1/ontology/stats", "v", viewer, "", nil); c != 403 {
		t.Errorf("viewer stats = %d", c)
	}
	if c := f.asTenant(t, "GET", "/api/v1/ontology/stats", "ann", "alpha", approver, "", nil); c != 403 {
		t.Errorf("tenant stats = %d", c)
	}
}
