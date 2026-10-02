// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package gaps

import (
	"math"
	"testing"

	"github.com/zyvorai/zyntra/internal/graph"
)

//go:fix inline
func f(v float64) *float64 { return new(v) }

func TestSeverity(t *testing.T) {
	cases := []struct {
		k    graph.KPI
		v    float64
		want float64
	}{
		{graph.KPI{Target: f(20), Direction: graph.HigherIsBetter}, 10, 0.5},
		{graph.KPI{Target: f(20), Direction: graph.HigherIsBetter}, 25, 0},
		{graph.KPI{Target: f(300), Direction: graph.LowerIsBetter}, 420, 0.4},
		{graph.KPI{Target: f(300), Direction: graph.LowerIsBetter}, 300, 0},
		{graph.KPI{Target: f(0), Direction: graph.LowerIsBetter}, 2, 2},
		{graph.KPI{}, 123, 0},
	}
	for i, c := range cases {
		if got := Severity(c.k, c.v); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

func TestDetectSortsWorstFirst(t *testing.T) {
	m, err := graph.Parse([]byte(`
kpis:
  - {id: ok, value: 1, target: 2, direction: lower}
  - {id: small, value: 11, target: 10, direction: lower}
  - {id: big, value: 5, target: 10, direction: higher}
`))
	if err != nil {
		t.Fatal(err)
	}
	g := Detect(m)
	if len(g) != 2 || g[0].KPI != "big" || g[1].KPI != "small" {
		t.Fatalf("got %+v", g)
	}
	if got := Total(m, nil); math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("total = %v", got)
	}
	if got := Total(m, map[string]float64{"big": 10}); math.Abs(got-0.1) > 1e-9 {
		t.Fatalf("total with override = %v", got)
	}
}
