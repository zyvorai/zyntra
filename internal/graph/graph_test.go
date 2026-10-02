// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package graph

import (
	"strings"
	"testing"
)

const valid = `
name: t
kpis:
  - {id: a, value: 10}
  - {id: b, value: 5, target: 4, direction: lower}
  - {id: c, value: 1}
edges:
  - {from: a, to: b, weight: 0.5}
  - {from: b, to: c, weight: 1}
actions:
  - id: x
    name: X
    effects: [{kpi: a, change: 0.1}]
`

func TestParseValid(t *testing.T) {
	m, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if k, ok := m.KPI("b"); !ok || *k.Target != 4 {
		t.Fatalf("kpi b not loaded: %+v", k)
	}
	if k, _ := m.KPI("a"); k.Name != "a" {
		t.Fatalf("name should default to id, got %q", k.Name)
	}
	order, err := m.TopoOrder()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "a,b,c" {
		t.Fatalf("order = %v", order)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"cycle": `
kpis: [{id: a, value: 1}, {id: b, value: 1}]
edges: [{from: a, to: b, weight: 1}, {from: b, to: a, weight: 1}]`,
		"unknown kpi": `
kpis: [{id: a, value: 1}]
edges: [{from: a, to: z, weight: 1}]`,
		"duplicate": `
kpis: [{id: a, value: 1}, {id: a, value: 2}]`,
		"target without direction": `
kpis: [{id: a, value: 1, target: 2}]`,
		"bad direction": `
kpis: [{id: a, value: 1, target: 2, direction: up}]`,
		"action unknown kpi": `
kpis: [{id: a, value: 1}]
actions: [{id: x, effects: [{kpi: z, change: 1}]}]`,
		"action no effects": `
kpis: [{id: a, value: 1}]
actions: [{id: x}]`,
		"bad risk": `
kpis: [{id: a, value: 1}]
actions: [{id: x, risk: extreme, effects: [{kpi: a, change: 1}]}]`,
		"bad criticality": `
kpis: [{id: a, value: 1, criticality: urgent}]`,
		"min above max": `
kpis: [{id: a, value: 1, min: 5, max: 1}]`,
		"bad duration": `
kpis: [{id: a, value: 1, freshness: {maxAge: soon}}]`,
		"constraint unknown kpi": `
kpis: [{id: a, value: 1}]
constraints: [{kpi: z, floor: 1}]`,
		"empty constraint": `
kpis: [{id: a, value: 1}]
constraints: [{kpi: a}]`,
		"mustNotWorsen without direction": `
kpis: [{id: a, value: 1}]
constraints: [{kpi: a, mustNotWorsen: true}]`,
		"bad effect mode": `
kpis: [{id: a, value: 1}]
actions: [{id: x, effects: [{kpi: a, change: 1, mode: percent}]}]`,
		"bad provenance": `
kpis: [{id: a, value: 1}, {id: b, value: 1}]
edges: [{from: a, to: b, weight: 1, provenance: guessed}]`,
		"bad criterion op": `
kpis: [{id: a, value: 1}]
actions: [{id: x, effects: [{kpi: a, change: 1}], outcome: {successCriteria: [{kpi: a, op: "==", value: 1}]}}]`,
		"bad keep mode": `
kpis: [{id: a, value: 1}]
actions: [{id: x, effects: [{kpi: a, change: 1}], policy: {keep: maybe}}]`,
	}
	for name, src := range cases {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestCloneIsDeep(t *testing.T) {
	m, err := Parse([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	c := m.Clone()
	c.KPIs[1].Value = 99
	*c.KPIs[1].Target = 99
	c.Actions[0].Effects[0].Change = 9
	if m.KPIs[1].Value != 5 || *m.KPIs[1].Target != 4 || m.Actions[0].Effects[0].Change != 0.1 {
		t.Fatal("clone shares state with original")
	}
}

const rich = `
name: rich
kpis:
  - {id: avail, value: 99.95, target: 99.9, direction: higher, criticality: critical, max: 100, freshness: {maxAge: 2m, required: true}}
  - {id: lat, value: 400, target: 300, direction: lower, criticality: critical}
  - {id: cost, value: 100, target: 120, direction: lower, criticality: low, min: 0}
edges:
  - {from: lat, to: avail, weight: -0.01, confidence: 0.3, provenance: learned, delay: 5m}
constraints:
  - {kpi: cost, ceiling: 150, why: budget}
actions:
  - id: x
    effects: [{kpi: lat, change: -50, mode: absolute, saturation: 80, uncertainty: 0.2, delay: 1m}]
    rollback: {template: gravia.job-suspend, params: {job: j, namespace: n, suspend: "false"}}
    outcome: {window: 10m, samples: 3, successCriteria: [{kpi: lat, op: "<=", value: 300}, {kpi: avail, op: met}], guardrails: [cost]}
    policy: {approvals: 2, keep: required, maintenanceWindows: [nightly]}
`

func TestRichModel(t *testing.T) {
	m, err := Parse([]byte(rich))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := m.KPI("avail")
	if a.Weight() != 4 || a.Freshness.MaxAge.D().Minutes() != 2 || !a.Freshness.Required || a.Clamp(101) != 100 {
		t.Fatalf("avail %+v", a)
	}
	if c, _ := m.KPI("cost"); c.Weight() != 0.5 {
		t.Fatalf("cost weight %v", c.Weight())
	}
	x, _ := m.Action("x")
	if x.Effects[0].Mode != EffectAbsolute || x.Outcome.Window.D().Minutes() != 10 || x.Policy.Approvals != 2 {
		t.Fatalf("action %+v", x)
	}

	cs := m.AllConstraints()
	if len(cs) != 3 {
		t.Fatalf("constraints %+v", cs)
	}
	// avail meets its critical target: implicit floor. lat misses: must not worsen.
	if cs[1].KPI != "avail" || cs[1].Floor == nil || *cs[1].Floor != 99.9 {
		t.Fatalf("avail implicit %+v", cs[1])
	}
	if cs[2].KPI != "lat" || !cs[2].MustNotWorsen {
		t.Fatalf("lat implicit %+v", cs[2])
	}
	lat, _ := m.KPI("lat")
	if _, bad := cs[2].Check(*lat, 400, 420); !bad {
		t.Fatal("lat worsening should violate")
	}
	if _, bad := cs[2].Check(*lat, 400, 350); bad {
		t.Fatal("lat improving should not violate")
	}
	cost, _ := m.KPI("cost")
	if v, bad := cs[0].Check(*cost, 100, 160); !bad || v.Kind != "ceiling" {
		t.Fatalf("cost ceiling %+v", v)
	}
	if _, bad := cs[0].Check(*cost, 170, 160); bad {
		t.Fatal("reducing an existing breach should not violate")
	}
	if _, bad := cs[0].Check(*cost, 170, 180); !bad {
		t.Fatal("worsening an existing breach should violate")
	}
}

func TestVersionIgnoresValues(t *testing.T) {
	m, err := Parse([]byte(rich))
	if err != nil {
		t.Fatal(err)
	}
	v := m.Version()
	m.KPIs[0].Value = 12
	if m.Version() != v {
		t.Fatal("version changed with a live value")
	}
	m.Edges[0].Weight = -0.02
	if m.Version() == v {
		t.Fatal("version did not change with an edge weight")
	}
}

func TestCloneRichIsDeep(t *testing.T) {
	m, err := Parse([]byte(rich))
	if err != nil {
		t.Fatal(err)
	}
	c := m.Clone()
	*c.KPIs[0].Max = 1
	c.KPIs[0].Freshness.Required = false
	*c.Constraints[0].Ceiling = 1
	c.Actions[0].Rollback.Params["job"] = "other"
	*c.Actions[0].Outcome.Success[0].Value = 1
	c.Actions[0].Policy.Windows[0] = "never"
	if *m.KPIs[0].Max != 100 || !m.KPIs[0].Freshness.Required || *m.Constraints[0].Ceiling != 150 ||
		m.Actions[0].Rollback.Params["job"] != "j" || *m.Actions[0].Outcome.Success[0].Value != 300 ||
		m.Actions[0].Policy.Windows[0] != "nightly" {
		t.Fatal("clone shares state with original")
	}
}

func TestTenantKPIs(t *testing.T) {
	base := "kpis:\n  - {id: a, value: 1, tenant: alpha}\n  - {id: b, value: 1, tenant: beta}\n  - {id: g, value: 1}\n"
	ok := base + "edges:\n  - {from: g, to: a, weight: 0.1}\n"
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("provider and tenant KPIs may be linked: %v", err)
	}
	cases := map[string]string{
		"cross-tenant edge": base + "edges:\n  - {from: a, to: b, weight: 0.1}\n",
		"bad tenant id":     "kpis:\n  - {id: a, value: 1, tenant: Alpha.Co}\n",
	}
	for name, text := range cases {
		if _, err := Parse([]byte(text)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
