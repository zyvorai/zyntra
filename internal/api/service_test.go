// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
	"github.com/zyvorai/zyntra/internal/auth"
	"github.com/zyvorai/zyntra/internal/ontology"
)

func TestServiceTokensReadAndProposeButNeverApprove(t *testing.T) {
	f := ontSetup(t, nil)
	tokP, hashP := auth.NewServiceToken()
	tokV, hashV := auth.NewServiceToken()
	tokOld, hashOld := auth.NewServiceToken()
	if err := f.s.opt.Auth.SetServiceTokens([]auth.ServiceCredential{
		{Name: "agent", TokenHash: hashP, Roles: []auth.Role{auth.RoleProposer}},
		{Name: "reader", TokenHash: hashV, Roles: []auth.Role{auth.RoleViewer}},
		{Name: "old", TokenHash: hashOld, Roles: []auth.Role{auth.RoleProposer}, NotAfter: time.Now().Add(-time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	var list struct{ Objects []ontology.Object }
	if c := f.do(t, "GET", "/api/v1/ontology/objects", tokP, "", &list); c != 200 || len(list.Objects) == 0 {
		t.Fatalf("proposer token read objects = %d (%d objects)", c, len(list.Objects))
	}
	if c := f.do(t, "GET", "/api/v1/ontology/objects", tokV, "", nil); c != 200 {
		t.Fatalf("viewer token read objects = %d", c)
	}
	var p approvals.Proposal
	if c := f.do(t, "POST", "/api/v1/proposals", tokP, `{"action":"free_action"}`, &p); c != 201 {
		t.Fatalf("proposer token proposal = %d", c)
	}
	if p.CreatedBy != "service:agent" {
		t.Errorf("proposal attributed to %q, want service:agent", p.CreatedBy)
	}
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/approve", tokP, `{}`, nil); c != 403 {
		t.Errorf("a service token approved its own proposal = %d, want 403", c)
	}
	if c := f.do(t, "POST", "/api/v1/proposals/"+p.ID+"/reject", tokP, `{}`, nil); c != 403 {
		t.Errorf("a service token rejected a proposal = %d, want 403", c)
	}
	if c := f.do(t, "POST", "/api/v1/proposals", tokV, `{"action":"free_action"}`, nil); c != 403 {
		t.Errorf("a viewer token proposed = %d, want 403", c)
	}
	// A typed action that names approver in its permissions stays out of reach.
	if c := f.do(t, "POST", "/api/v1/proposals", tokP, `{"action":"add_gpus","inputs":{"cluster":"Cluster:x:c1"}}`, nil); c != 422 {
		t.Errorf("proposer token on an approver-only typed action = %d, want 422", c)
	}
	if c := f.do(t, "GET", "/api/v1/ontology/objects", tokOld, "", nil); c != 401 {
		t.Errorf("an expired service token = %d, want 401", c)
	}
	if c := f.do(t, "GET", "/api/v1/ontology/objects", "zst_"+strings.Repeat("0", 64), "", nil); c != 401 {
		t.Errorf("an unknown service token = %d, want 401", c)
	}
}

func TestTenantServiceTokenSeesOnlyItsTenant(t *testing.T) {
	f := tenantSetup(t)
	tok, hash := auth.NewServiceToken()
	if err := f.s.opt.Auth.SetServiceTokens([]auth.ServiceCredential{
		{Name: "alpha-agent", TokenHash: hash, Roles: []auth.Role{auth.RoleViewer}, Tenant: "alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	var list struct{ Objects []ontology.Object }
	if c := f.do(t, "GET", "/api/v1/ontology/objects", tok, "", &list); c != 200 {
		t.Fatalf("tenant token read objects = %d", c)
	}
	for _, o := range list.Objects {
		if o.Tenant != "alpha" {
			t.Errorf("tenant token saw %s of tenant %q", o.ID, o.Tenant)
		}
	}
	if len(list.Objects) == 0 {
		t.Error("tenant token saw none of its own objects")
	}
	if c := f.do(t, "GET", "/api/v1/gaps", tok, "", nil); c != 403 {
		t.Errorf("tenant token read the KPI gaps = %d, want 403", c)
	}
}
