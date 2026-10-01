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
