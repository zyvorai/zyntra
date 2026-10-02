// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWithTenantRefusesDeploymentWideRoles(t *testing.T) {
	for _, roles := range [][]Role{{RoleAdmin}, {RoleExecutor}, {RoleViewer, RoleAdmin}, {RoleIngest}, {RoleExec}} {
		if _, err := newIdentity("u", "password", roles...).WithTenant("alpha"); err == nil {
			t.Errorf("roles %v were allowed to be tenant-bound", roles)
		}
	}
	for _, role := range TenantRoles {
		if id, err := newIdentity("u", "password", role).WithTenant("alpha"); err != nil || id.Tenant != "alpha" {
			t.Errorf("%s: %v", role, err)
		}
	}
	for _, bad := range []string{"", "Alpha", "a.b", "-x", "a b", strings.Repeat("a", 41), "a\x00"} {
		if _, err := newIdentity("u", "password", RoleViewer).WithTenant(bad); err == nil {
			t.Errorf("tenant %q accepted", bad)
		}
	}
}

func TestSessionCookieCarriesAndProtectsTenant(t *testing.T) {
	a := New("k", "")
	id, _ := newIdentity("ann", "password", RoleApprover).WithTenant("alpha")
	cookie, _ := a.Mint(time.Hour, id)
	got, ok := a.Valid(cookie)
	if !ok || got.Tenant != "alpha" || got.Subject != "ann" {
		t.Fatalf("round trip: %+v %v", got, ok)
	}
	// Swapping the tenant, or dropping it, breaks the signature.
	parts := strings.Split(cookie, ".")
	for _, swap := range []string{"beta", "-"} {
		forged := append([]string(nil), parts...)
		forged[3] = swap
		if _, ok := a.Valid(strings.Join(forged, ".")); ok {
			t.Errorf("a cookie with tenant %q was accepted", swap)
		}
	}
	// A deployment-wide identity round-trips with no tenant.
	plain, _ := a.Mint(time.Hour, newIdentity("root", "key", RoleAdmin))
	if got, ok := a.Valid(plain); !ok || got.Tenant != "" {
		t.Errorf("plain identity: %+v %v", got, ok)
	}
	// An operator name with dots still parses.
	dotted, _ := newIdentity("ann.smith@x.io", "password", RoleViewer).WithTenant("alpha")
	c2, _ := a.Mint(time.Hour, dotted)
	if got, ok := a.Valid(c2); !ok || got.Subject != "ann.smith@x.io" || got.Tenant != "alpha" {
		t.Errorf("dotted operator: %+v %v", got, ok)
	}
	// A pre-tenant (v2) cookie no longer validates.
	old := strings.Join([]string{"9999999999", "password", "viewer", "ann", a.mac("zyntra-session.v2", "9999999999", "password", "viewer", "ann")}, ".")
	if _, ok := a.Valid(old); ok {
		t.Error("a v2 cookie was accepted")
	}
}

func TestConnectorCredentialLifecycle(t *testing.T) {
	a := New("k", "")
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	tok1, h1 := NewConnectorToken()
	tok2, h2 := NewConnectorToken()
	if !strings.HasPrefix(tok1, TokenPrefix) || h1 == HashToken(tok2) || len(h1) != 64 {
		t.Fatalf("token/hash shape: %q %q", tok1, h1)
	}
	if err := a.SetCredentials([]IngestCredential{
		// Rotation: the old token keeps working until its NotAfter.
		{Name: "mes", TokenHash: h1, Tenants: []string{"alpha"}, NotAfter: now.Add(2 * time.Hour)},
		{Name: "mes", TokenHash: h2, Tenants: []string{"alpha"}, NotBefore: now.Add(-time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	ident := func(tok string) Identity {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.Identify(r)
	}
	for _, tok := range []string{tok1, tok2} {
		id := ident(tok)
		if id.Subject != "connector:mes" || id.Scope == nil || !id.Has(RoleIngest) || id.Has(RoleViewer, RoleAdmin) {
			t.Fatalf("identity = %+v", id)
		}
		if !id.Scope.Allows("alpha") || id.Scope.Allows("beta") || id.Scope.Allows("") {
			t.Errorf("scope = %+v", id.Scope)
		}
	}
	now = now.Add(3 * time.Hour) // the old token has expired, the new one has not
	if ident(tok1).Role != RoleNone {
		t.Error("an expired token still authenticates")
	}
	if ident(tok2).Role != RoleIngest {
		t.Error("the replacement token stopped working")
	}
	if ident("zct_nope").Role != RoleNone || ident(strings.TrimPrefix(tok2, TokenPrefix)).Role != RoleNone {
		t.Error("a wrong or unprefixed token authenticated")
	}
	a.creds[1].Revoked = true
	if ident(tok2).Role != RoleNone {
		t.Error("a revoked token still authenticates")
	}
	// A connector token is not a cookie identity: it can never be minted into one.
	if (IngestScope{Tenants: []string{"default"}}).Allows("") != true {
		t.Error("the default tenant should be addressable as \"default\"")
	}
}

func TestCredentialValidation(t *testing.T) {
	_, h := NewConnectorToken()
	good := IngestCredential{Name: "mes", TokenHash: h, Tenants: []string{"alpha"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*IngestCredential){
		"name":       func(c *IngestCredential) { c.Name = "Bad Name" },
		"short hash": func(c *IngestCredential) { c.TokenHash = "abc" },
		"non-hex":    func(c *IngestCredential) { c.TokenHash = strings.Repeat("z", 64) },
		"no tenants": func(c *IngestCredential) { c.Tenants = nil },
		"bad tenant": func(c *IngestCredential) { c.Tenants = []string{"A.B"} },
		"window":     func(c *IngestCredential) { c.NotBefore, c.NotAfter = time.Now(), time.Now().Add(-time.Hour) },
	}
	for name, mut := range bad {
		c := good
		mut(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
