// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package freshness

import (
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/graph"
)

func TestStates(t *testing.T) {
	m, err := graph.Parse([]byte(`
kpis:
  - {id: live, value: 1, source: {kind: prometheus, query: q}}
  - {id: tight, value: 1, source: {kind: prometheus, query: q}, freshness: {maxAge: 10s, required: true}}
  - {id: never, value: 1, source: {kind: prometheus, query: q}}
  - {id: static, value: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	tr := New(time.Minute)
	tr.SetClock(func() time.Time { return now })
	tr.Success("live", now.Add(-30*time.Second))
	tr.Success("tight", now.Add(-30*time.Second))
	tr.Failure("tight", "boom", now)

	idx := Index(tr.States(m))
	if s := idx["live"]; s.Status != Fresh || s.AgeSeconds != 30 {
		t.Fatalf("live %+v", s)
	}
	if s := idx["tight"]; s.Status != Stale || !s.Required || s.LastError != "boom" {
		t.Fatalf("tight %+v", s)
	}
	if s := idx["never"]; s.Status != Missing {
		t.Fatalf("never %+v", s)
	}
	if s := idx["static"]; s.Status != Static || !s.Usable() {
		t.Fatalf("static %+v", s)
	}
	if u := Unusable(tr.States(m)); len(u) != 2 || u[0] != "never" || u[1] != "tight" {
		t.Fatalf("unusable %v", u)
	}

	tr.Success("tight", now)
	if s := Index(tr.States(m))["tight"]; s.Status != Fresh || s.LastError != "" {
		t.Fatalf("recovered %+v", s)
	}
	var nilTracker *Tracker
	if s := nilTracker.States(m); s[0].Status != Static {
		t.Fatalf("nil tracker %+v", s)
	}
}
