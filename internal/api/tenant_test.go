// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/actions"
	"github.com/zyvorai/zyntra/internal/ai"
	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/decisions"
	"github.com/zyvorai/zyntra/internal/ontology"
	"github.com/zyvorai/zyntra/internal/scenario"
)

func tenantSetup(t *testing.T) *fixture {
	t.Helper()
	def, err := ontology.ParseDefinition([]byte(ontDef))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", def.Schema())
	rec := func(tenant, key, name string) ontology.Record {
		return ontology.Record{Type: "Cluster", Namespace: "x", Key: key, Tenant: tenant,
			Props: map[string]any{"name": name, "status": "active"}}
	}
	cust := func(tenant, key, name, cluster string) ontology.Record {
		return ontology.Record{Type: "Customer", Namespace: "x", Key: key, Tenant: tenant,
			Props: map[string]any{"name": name, "contact": name + "@example.com"},
			Links: []ontology.RecordLink{{Type: "serves", ToType: "Cluster", ToNS: "x", ToKey: cluster}}}
	}
	if _, err := st.Ingest("seed", "t", []ontology.Record{
		rec("alpha", "ca", "Alpha Cluster"), rec("beta", "cb", "Beta Cluster"), rec("", "cs", "Shared Cluster"),
		cust("alpha", "ka", "Alpha Co", "ca"), cust("beta", "kb", "Beta Co", "cb"),
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	ac := &ontology.Access{Schema: def.Schema()}
	scn, _ := scenario.Open("")
	return setupWith(t, ontModel, func(o *Options) {
		o.Ontology = OntologyOptions{Def: def, Store: st, Access: ac, Actions: actions.New(def, st, ac), Scenarios: scn}
	})
}

// asTenant performs a request as a tenant-bound identity.
func (f *fixture) asTenant(t *testing.T, method, path, user, tenant string, roles []auth.Role, body string, out any) int {
	t.Helper()
	id, err := auth.Identity{Subject: user, Roles: roles, Method: "password"}.WithTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := f.s.opt.Auth.Mint(time.Hour, id)
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
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func TestTenantSeesOnlyItsObjects(t *testing.T) {
	f := tenantSetup(t)
	var list struct{ Objects []ontology.Object }
	f.asTenant(t, "GET", "/api/v1/ontology/objects", "ann", "alpha", approver, "", &list)
	if len(list.Objects) != 2 {
		t.Fatalf("alpha saw %d objects: %+v", len(list.Objects), list.Objects)
	}
	for _, o := range list.Objects {
		if o.Tenant != "alpha" {
			t.Errorf("leaked %s (tenant %q)", o.ID, o.Tenant)
		}
	}
	for _, id := range []string{"Cluster:x:cb", "Customer:x:kb", "Cluster:x:cs"} {
		if c := f.asTenant(t, "GET", "/api/v1/ontology/objects/"+id, "ann", "alpha", approver, "", nil); c != 404 {
			t.Errorf("object %s outside the tenant = %d, want 404", id, c)
		}
	}
	var imp struct{ Impact []ontology.Impact }
	f.asTenant(t, "GET", "/api/v1/ontology/objects/Cluster:x:ca/impact", "ann", "alpha", approver, "", &imp)
	if len(imp.Impact) != 1 || imp.Impact[0].Object.ID != "Customer:x:ka" {
		t.Errorf("impact = %+v", imp.Impact)
	}
	var all struct{ Objects []ontology.Object }
	f.as(t, "GET", "/api/v1/ontology/objects", "root", []auth.Role{auth.RoleAdmin}, "", &all)
	if len(all.Objects) != 5 {
		t.Errorf("a deployment-wide admin should see all 5 objects, saw %d", len(all.Objects))
	}
}

func TestTenantGateIsDenyByDefault(t *testing.T) {
	f := tenantSetup(t)
	denied := []struct{ method, path string }{
		{"GET", "/api/v1/graph"}, {"GET", "/api/v1/gaps"}, {"GET", "/api/v1/plan"}, {"POST", "/api/v1/simulate"},
		{"GET", "/api/v1/sources"}, {"GET", "/api/v1/freshness"}, {"GET", "/api/v1/events"}, {"GET", "/api/v1/inputs"},
		{"GET", "/api/v1/policy"}, {"GET", "/api/v1/audit/verify"}, {"GET", "/api/v1/ai/digest"}, {"GET", "/api/v1/ai/insights"},
		{"POST", "/api/v1/ai/explain"}, {"GET", "/api/v1/similar"}, {"GET", "/api/v1/scenarios"}, {"POST", "/api/v1/scenarios"},
		{"POST", "/api/v1/ontology/refresh"}, {"POST", "/api/v1/ontology/ingest/alpha"}, {"GET", "/api/v1/keep/status"},
		{"POST", "/api/v1/exec/x"}, {"GET", "/api/v1/decisions/x/export"}, {"GET", "/api/v1/proposals/x/explanation"},
		{"GET", "/api/v1/proposals/x/similar"}, {"POST", "/api/v1/kpis/queue/value"}, {"DELETE", "/api/v1/proposals/x"},
	}
	for _, d := range denied {
		if c := f.asTenant(t, d.method, d.path, "ann", "alpha", approver, "{}", nil); c != 403 {
			t.Errorf("%s %s as a tenant account = %d, want 403", d.method, d.path, c)
		}
	}
	// The same routes still work for a deployment-wide reader.
	if c := f.as(t, "GET", "/api/v1/gaps", "v", viewer, "", nil); c != 200 {
		t.Errorf("deployment-wide viewer lost /gaps: %d", c)
	}
	// And the console shell and whoami stay reachable.
	var who struct{ Identity auth.Identity }
	if c := f.asTenant(t, "GET", "/api/v1/whoami", "ann", "alpha", approver, "", &who); c != 200 || who.Identity.Tenant != "alpha" {
		t.Errorf("whoami = %d %+v", c, who.Identity)
	}
}

func TestTenantProposalsAreIsolatedAndTrimmed(t *testing.T) {
	f := tenantSetup(t)
	var p approvals.Proposal
	if c := f.asTenant(t, "POST", "/api/v1/proposals", "ann", "alpha", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:ca"}}`, &p); c != 201 {
		t.Fatalf("alpha proposing on its own cluster = %d", c)
	}
	if p.Tenant != "alpha" || len(p.Objects) != 1 {
		t.Fatalf("proposal = %+v", p)
	}
	if p.Simulation != nil || p.Inputs != nil || len(p.Predicted.KPIs) != 0 || len(p.Baseline) != 0 || p.Render != "" || len(p.Alternatives) != 0 {
		t.Errorf("a tenant must not see KPI values, simulation or rendered payloads: %+v", p)
	}
	// Other tenants' objects, shared objects alone, and untyped actions are refused.
	for name, body := range map[string]string{
		"another tenant's object": `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:cb"}}`,
		"a shared object only":    `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:cs"}}`,
	} {
		if c := f.asTenant(t, "POST", "/api/v1/proposals", "ann", "alpha", approver, body, nil); c != 422 {
			t.Errorf("%s = %d, want 422", name, c)
		}
	}
	if c := f.asTenant(t, "POST", "/api/v1/proposals", "ann", "alpha", approver, `{"action":"free_action"}`, nil); c != 403 {
		t.Errorf("an untyped action = %d, want 403", c)
	}
	// Beta cannot see, read, approve or reject it; it is indistinguishable from missing.
	var beta struct{ Proposals []approvals.Proposal }
	f.asTenant(t, "GET", "/api/v1/proposals", "bob", "beta", approver, "", &beta)
	if len(beta.Proposals) != 0 {
		t.Errorf("beta sees alpha's proposals: %+v", beta.Proposals)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/proposals/" + p.ID}, {"POST", "/api/v1/proposals/" + p.ID + "/approve"},
		{"POST", "/api/v1/proposals/" + p.ID + "/reject"}, {"GET", "/api/v1/decisions/" + p.ID},
	} {
		if code := f.asTenant(t, c.method, c.path, "bob", "beta", approver, "{}", nil); code != 404 {
			t.Errorf("beta %s %s = %d, want 404", c.method, c.path, code)
		}
	}
	// Alpha's own approver can decide it.
	var done approvals.Proposal
	if c := f.asTenant(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", "ann", "alpha", approver, `{}`, &done); c != 200 {
		t.Fatalf("alpha approve = %d", c)
	}
	if done.Status != approvals.Executed || done.Execution != nil || done.Render != "" {
		t.Errorf("approved view leaks execution detail or has the wrong status: %+v", done)
	}
	// A deployment-wide admin still sees the whole thing.
	var full approvals.Proposal
	f.as(t, "GET", "/api/v1/proposals/"+p.ID, "root", []auth.Role{auth.RoleAdmin}, "", &full)
	if full.Simulation == nil || full.Render == "" || full.Tenant != "alpha" {
		t.Errorf("admin view was trimmed: %+v", full)
	}
	var list struct{ Proposals []approvals.Proposal }
	f.as(t, "GET", "/api/v1/proposals", "root", []auth.Role{auth.RoleAdmin}, "", &list)
	if len(list.Proposals) != 1 {
		t.Errorf("admin list = %d", len(list.Proposals))
	}
	// The signed export stays with deployment-wide roles.
	var ex decisions.Export
	if c := f.as(t, "GET", "/api/v1/decisions/"+p.ID+"/export", "root", []auth.Role{auth.RoleAdmin}, "", &ex); c != 200 || ex.Decision.Tenant != "alpha" {
		t.Errorf("export = %d tenant %q", c, ex.Decision.Tenant)
	}
}

func TestTenantAuditAndDecisionsAreFiltered(t *testing.T) {
	f := tenantSetup(t)
	var a, b approvals.Proposal
	f.asTenant(t, "POST", "/api/v1/proposals", "ann", "alpha", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:ca"}}`, &a)
	f.asTenant(t, "POST", "/api/v1/proposals", "bob", "beta", approver, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:cb"}}`, &b)
	f.as(t, "POST", "/api/v1/proposals", "p", proposer, `{"action":"free_action"}`, nil)
	var audit struct {
		Events []approvals.Event
		Chain  *approvals.Verification
	}
	f.asTenant(t, "GET", "/api/v1/audit", "ann", "alpha", approver, "", &audit)
	if len(audit.Events) == 0 || audit.Chain != nil {
		t.Fatalf("tenant audit = %+v", audit)
	}
	for _, e := range audit.Events {
		if e.Proposal != a.ID {
			t.Errorf("tenant audit includes %s", e.Proposal)
		}
		if e.PrevHash != "" || e.Hash != "" || e.Seq != 0 {
			t.Errorf("hash-chain fields leaked: %+v", e)
		}
	}
	var dec struct {
		Decisions []approvals.Proposal
		Chain     *approvals.Verification
	}
	f.asTenant(t, "GET", "/api/v1/decisions", "ann", "alpha", approver, "", &dec)
	if len(dec.Decisions) != 1 || dec.Decisions[0].ID != a.ID || dec.Chain != nil {
		t.Errorf("tenant decisions = %+v", dec)
	}
	var global struct {
		Events []approvals.Event
		Chain  *approvals.Verification
	}
	f.as(t, "GET", "/api/v1/audit", "root", []auth.Role{auth.RoleAdmin}, "", &global)
	if global.Chain == nil || len(global.Events) <= len(audit.Events) {
		t.Errorf("the global audit lost its chain or events: %+v", global.Chain)
	}
}

func TestTenantAskIsObjectsOnly(t *testing.T) {
	f := tenantSetup(t)
	var gaps ai.Answer
	f.asTenant(t, "POST", "/api/v1/ai/ask", "ann", "alpha", approver, `{"question":"What are the biggest gaps right now and what should we do?"}`, &gaps)
	if gaps.Intent != "scope" || strings.Contains(gaps.Text, "queue") || strings.Contains(gaps.Text, "add_gpus") {
		t.Fatalf("a tenant got a deployment-wide answer: %+v", gaps)
	}
	var objs ai.Answer
	f.asTenant(t, "POST", "/api/v1/ai/ask", "ann", "alpha", approver, `{"question":"Which customers are at risk?"}`, &objs)
	if !strings.Contains(objs.Text, "Alpha Co") || strings.Contains(objs.Text, "Beta") {
		t.Fatalf("tenant object answer wrong: %s", objs.Text)
	}
	for _, c := range objs.Citations {
		if strings.Contains(c.Object, ":kb") || strings.Contains(c.Object, ":cb") {
			t.Errorf("citation outside the tenant: %+v", c)
		}
	}
}

func TestSchemaHidesConnectorsAndTenantRollout(t *testing.T) {
	f := tenantSetup(t)
	f.s.opt.Ontology.Def.Connectors = []ontology.ConnectorSpec{{Name: "x", Kind: "exec", Command: []string{"/secret/bin"}}}
	var raw map[string]any
	f.as(t, "GET", "/api/v1/ontology/schema", "v", viewer, "", &raw)
	if _, ok := raw["connectors"]; ok {
		t.Error("connector commands are exposed to readers")
	}
	if _, ok := raw["rollout"]; !ok {
		t.Error("deployment-wide readers should still see the rollout shape")
	}
	var tr map[string]any
	f.asTenant(t, "GET", "/api/v1/ontology/schema", "ann", "alpha", viewer, "", &tr)
	if _, ok := tr["rollout"]; ok {
		t.Error("a tenant sees rollout sites")
	}
}

func TestConnectorCredentialsAreScoped(t *testing.T) {
	f := tenantSetup(t)
	f.s.opt.Ontology.Store.Audit = func(subject, by, note string) { _ = f.s.opt.Store.Note(subject, by, note) }
	tokA, hashA := auth.NewConnectorToken()
	tokOld, hashOld := auth.NewConnectorToken()
	tokB, hashB := auth.NewConnectorToken()
	now := time.Now()
	if err := f.s.opt.Auth.SetCredentials([]auth.IngestCredential{
		{Name: "mes-alpha", TokenHash: hashA, Tenants: []string{"alpha"}, Types: []string{"Cluster"}},
		{Name: "mes-old", TokenHash: hashOld, Tenants: []string{"alpha"}, NotAfter: now.Add(-time.Hour)},
		{Name: "mes-beta", TokenHash: hashB, Tenants: []string{"beta"}, Revoked: true},
	}); err != nil {
		t.Fatal(err)
	}
	cluster := `{"source":"mes","records":[{"type":"Cluster","namespace":"mes","key":"n1","props":{"name":"New","status":"active"}}]}`
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", tokA, cluster, nil); c != 202 {
		t.Fatalf("scoped credential into its tenant = %d", c)
	}
	o, _ := f.s.opt.Ontology.Store.Get("Cluster:mes:n1")
	if o.Tenant != "alpha" || o.Props["name"].Prov.Source != "push:mes" {
		t.Errorf("pushed object = %+v", o)
	}
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/beta", tokA, cluster, nil); c != 403 {
		t.Errorf("another tenant = %d, want 403", c)
	}
	customer := `{"source":"mes","records":[{"type":"Customer","namespace":"mes","key":"c1","props":{"name":"X"}}]}`
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", tokA, customer, nil); c != 403 {
		t.Errorf("a type outside the credential = %d, want 403", c)
	}
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", tokOld, cluster, nil); c != 401 {
		t.Errorf("an expired credential = %d, want 401", c)
	}
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/beta", tokB, cluster, nil); c != 401 {
		t.Errorf("a revoked credential = %d, want 401", c)
	}
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", "zct_"+strings.Repeat("0", 64), cluster, nil); c != 401 {
		t.Errorf("an unknown token = %d, want 401", c)
	}
	// It writes nothing but ingest: no reads, no proposals, no scenario runs.
	for _, path := range []string{"/api/v1/ontology/objects", "/api/v1/proposals", "/api/v1/gaps"} {
		if c := f.do(t, "GET", path, tokA, "", nil); c != 403 {
			t.Errorf("a connector token read %s = %d, want 403", path, c)
		}
	}
	// It cannot take over a record that belongs to another tenant.
	steal := `{"source":"mes","records":[{"type":"Cluster","namespace":"x","key":"cb","props":{"name":"stolen"}}]}`
	if c := f.do(t, "POST", "/api/v1/ontology/ingest/alpha", tokA, steal, nil); c != 422 {
		t.Errorf("taking over another tenant's id = %d, want 422", c)
	}
	if b, _ := f.s.opt.Ontology.Store.Get("Cluster:x:cb"); b.Props["name"].V != "Beta Cluster" {
		t.Error("another tenant's object was modified")
	}
	// The audit trail names the connector, not a shared identity.
	var audit struct{ Events []approvals.Event }
	f.as(t, "GET", "/api/v1/audit", "root", []auth.Role{auth.RoleAdmin}, "", &audit)
	found := false
	for _, e := range audit.Events {
		found = found || (e.By == "connector:mes-alpha" && strings.HasPrefix(e.Action, "ontology:push:mes"))
	}
	if !found {
		t.Errorf("no audit entry for connector:mes-alpha: %+v", audit.Events)
	}
}
