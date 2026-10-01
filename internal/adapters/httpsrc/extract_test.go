// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package httpsrc

import (
	"encoding/json"
	"testing"
)

const exposition = `# HELP netra_dns_failures DNS failures
# TYPE netra_dns_failures counter
netra_dns_failures{node="a"} 3
netra_dns_failures{node="b",zone="x\"y"} 4.5
netra_agents_stale 0
netra_ebpf_packets{dir="ingress",node="a"} 100 1700000000000
netra_ebpf_packets{dir="egress",node="a"} 50
bogus line
`

func TestParseAndAggregate(t *testing.T) {
	s := ParseMetrics([]byte(exposition))
	if len(s) != 5 {
		t.Fatalf("parsed %d samples: %+v", len(s), s)
	}
	if s[1].Labels["zone"] != `x"y` {
		t.Fatalf("escaped label = %q", s[1].Labels["zone"])
	}
	cases := []struct {
		name   string
		labels map[string]string
		agg    string
		want   float64
	}{
		{"netra_dns_failures", nil, "", 7.5},
		{"netra_dns_failures", nil, "max", 4.5},
		{"netra_dns_failures", map[string]string{"node": "a"}, "", 3},
		{"netra_ebpf_packets", map[string]string{"dir": "ingress"}, "first", 100},
		{"netra_ebpf_packets", nil, "count", 2},
		{"netra_agents_stale", nil, "avg", 0},
	}
	for _, c := range cases {
		got, err := Aggregate(s, c.name, c.labels, c.agg)
		if err != nil || got != c.want {
			t.Errorf("%s %v %s: got %v err %v, want %v", c.name, c.labels, c.agg, got, err, c.want)
		}
	}
	if _, err := Aggregate(s, "missing", nil, ""); err == nil {
		t.Fatal("expected not found")
	}
}

func TestField(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{
	  "utilizationPercent": 61.2, "ready": true, "count": "7",
	  "vms": [{"state":"Running","cost":2},{"state":"stopped","cost":3},{"state":"running"}],
	  "nested": {"list": [{"x": 9}]}
	}`), &v)
	cases := map[string]float64{
		"utilizationPercent":        61.2,
		"ready":                     1,
		"count":                     7,
		"vms.#":                     3,
		"vms.#(state=running)":      2,
		"vms.#(state=stopped).cost": 3,
		"vms.*.cost":                5,
		"nested.list.0.x":           9,
		"vms":                       3,
	}
	for path, want := range cases {
		got, err := Field(v, path)
		if err != nil || got != want {
			t.Errorf("%s: got %v err %v, want %v", path, got, err, want)
		}
	}
	for _, bad := range []string{"nope", "vms.9.state", "utilizationPercent.x", "vms.#(state)", "vms.#(state=gone).cost"} {
		if _, err := Field(v, bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}
