// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

const doc = `
maintenanceWindows:
  nightly:
    days: [mon, tue, wed, thu, fri]
    start: "22:00"
    end: "06:00"
    timezone: Europe/Berlin
rules:
  - name: high-risk-two-person
    match: {risk: [high]}
    approvals: 2
    distinctFromProposer: true
    maintenanceWindows: [nightly]
    keep: required
  - name: gravia-fresh
    match: {adapters: [gravia]}
    requireFresh: true
    approvedExpiry: 30m
revalidation:
  maxDrift: 0.3
`

func TestDefaultsKeepSingleApprover(t *testing.T) {
	p, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	e := p.For(graph.Action{ID: "a", Risk: graph.RiskHigh})
	if e.Approvals != 1 || e.DistinctFromProposer || e.Keep != KeepPreferred || e.RequireFresh || len(e.Windows) != 0 {
		t.Fatalf("defaults %+v", e)
	}
	if e.PendingExpiry != DefaultPendingExpiry || e.ApprovedExpiry != DefaultApprovedExpiry || p.MaxDrift() != DefaultMaxDrift {
		t.Fatalf("default expiry %+v", e)
	}
	if ok, _ := p.InWindow(nil, time.Now()); !ok {
		t.Fatal("no windows must allow any time")
	}
}

func TestRulesMergeAndActionOverride(t *testing.T) {
	p, err := Parse(doc)
	if err != nil {
		t.Fatal(err)
	}
	hi := graph.Action{ID: "spot", Risk: graph.RiskHigh, Adapter: "gravia"}
	e := p.For(hi)
	if e.Approvals != 2 || !e.DistinctFromProposer || e.Keep != KeepRequired || !e.RequireFresh || e.ApprovedExpiry != 30*time.Minute {
		t.Fatalf("high gravia %+v", e)
	}
	if len(e.Rules) != 2 || p.MaxDrift() != 0.3 {
		t.Fatalf("rules %v drift %v", e.Rules, p.MaxDrift())
	}
	lo := graph.Action{ID: "mig", Adapter: "netra", Policy: &graph.ActionPolicy{Approvals: 3, Keep: KeepOff}}
	if e := p.For(lo); e.Approvals != 3 || e.Keep != KeepOff || e.RequireFresh {
		t.Fatalf("action override %+v", e)
	}
	pair := p.For(hi, lo)
	if pair.Approvals != 3 || pair.Keep != KeepRequired || !pair.RequireFresh || pair.ApprovedExpiry != 30*time.Minute {
		t.Fatalf("pair takes strictest %+v", pair)
	}
}

func TestWindowsWrapMidnightInZone(t *testing.T) {
	p, _ := Parse(doc)
	berlin, _ := time.LoadLocation("Europe/Berlin")
	cases := map[time.Time]bool{
		time.Date(2026, 10, 5, 23, 0, 0, 0, berlin): true,  // Monday late
		time.Date(2026, 10, 6, 5, 59, 0, 0, berlin): true,  // Tuesday early, belongs to Monday
		time.Date(2026, 10, 6, 6, 0, 0, 0, berlin):  false, // window closed
		time.Date(2026, 10, 5, 12, 0, 0, 0, berlin): false,
		time.Date(2026, 10, 4, 23, 0, 0, 0, berlin): false, // Sunday not listed
		time.Date(2026, 10, 5, 3, 0, 0, 0, berlin):  false, // Monday early belongs to Sunday
		time.Date(2026, 10, 10, 2, 0, 0, 0, berlin): true,  // Saturday early belongs to Friday
	}
	for at, want := range cases {
		if got, why := p.InWindow([]string{"nightly"}, at); got != want {
			t.Errorf("%s: in=%v want %v (%s)", at, got, want, why)
		}
	}
}

func TestValidation(t *testing.T) {
	for _, bad := range []string{
		"rules: [{keep: maybe}]",
		"rules: [{maintenanceWindows: [nope]}]",
		"maintenanceWindows: {w: {start: '25:00', end: '01:00'}}",
		"maintenanceWindows: {w: {start: '01:00', end: '02:00', days: [funday]}}",
		"maintenanceWindows: {w: {start: '01:00', end: '02:00', timezone: Mars/Base}}",
		"revalidation: {maxDrift: 2}",
		"users: [{name: a}]",
		"unknown: 1",
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	p, _ := Parse(doc)
	m := &graph.Model{Actions: []graph.Action{{ID: "x", Policy: &graph.ActionPolicy{Windows: []string{"weekend"}}}}}
	if p.CheckModel(m) == nil {
		t.Fatal("unknown window in model accepted")
	}
}

func TestExamplePolicyLoads(t *testing.T) {
	p, err := Load("../../examples/policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := graph.Load("../../packs/gpu/kpis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckModel(m); err != nil {
		t.Fatal(err)
	}
	a, _ := m.Action("rebalance-host")
	if e := p.For(*a); e.Approvals != 2 || !e.DistinctFromProposer || len(e.Windows) != 2 {
		t.Fatalf("rebalance-host %+v", e)
	}
	g, _ := m.Action("raise-inference-priority")
	if e := p.For(*g); e.Keep != KeepRequired || !e.RequireFresh {
		t.Fatalf("gravia %+v", e)
	}
}

func TestTenantUsersAndConnectorsInPolicy(t *testing.T) {
	hash := strings.Repeat("a", 64)
	ok := `
users:
  - {name: ann, passwordHash: x, roles: [approver], tenant: alpha}
  - {name: root, passwordHash: x, roles: [admin]}
connectors:
  - {name: mes-alpha, token_sha256: ` + hash + `, tenants: [alpha], object_types: [Machine]}
  - {name: mes-alpha, token_sha256: ` + strings.Repeat("b", 64) + `, tenants: [alpha], not_after: 2027-01-01T00:00:00Z}
`
	p, err := Parse(ok)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Credentials(); len(got) != 2 || got[0].Types[0] != "Machine" || got[1].NotAfter.Year() != 2027 {
		t.Fatalf("credentials = %+v", got)
	}
	bad := map[string]string{
		"tenant admin":    "users:\n  - {name: a, passwordHash: x, roles: [admin], tenant: alpha}\n",
		"tenant executor": "users:\n  - {name: a, passwordHash: x, roles: [executor], tenant: alpha}\n",
		"bad tenant id":   "users:\n  - {name: a, passwordHash: x, roles: [viewer], tenant: Alpha.Co}\n",
		"short hash":      "connectors:\n  - {name: m, token_sha256: abc, tenants: [alpha]}\n",
		"no tenants":      "connectors:\n  - {name: m, token_sha256: " + hash + "}\n",
		"unknown field":   "connectors:\n  - {name: m, token: plaintext, token_sha256: " + hash + ", tenants: [a]}\n",
	}
	for name, text := range bad {
		if _, err := Parse(text); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
