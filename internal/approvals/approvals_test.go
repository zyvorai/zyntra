// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package approvals

import (
	"path/filepath"
	"testing"

	"github.com/zyvorai/zyntra/internal/executor"
)

func TestLifecycleAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, created, err := s.Create(Proposal{Action: "spot", ActionName: "Spot"}, "admin")
	if err != nil || !created || p.Status != Pending {
		t.Fatalf("create: %+v %v %v", p, created, err)
	}
	dup, created, _ := s.Create(Proposal{Action: "spot"}, "admin")
	if created || dup.ID != p.ID {
		t.Fatal("duplicate pending proposal created")
	}
	if _, err := s.Complete(p.ID, executor.Result{OK: true}, "x"); err == nil {
		t.Fatal("completed a pending proposal")
	}
	if _, err := s.Decide(p.ID, true, "admin", "looks good"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(p.ID, false, "admin", ""); err == nil {
		t.Fatal("decided twice")
	}
	done, err := s.Complete(p.ID, executor.Result{Mode: executor.ModeDryRun, OK: true}, "keep-broker")
	if err != nil || done.Status != Executed || done.Execution == nil {
		t.Fatalf("complete: %+v %v", done, err)
	}
	if err := s.RecordActual(p.ID, map[string]float64{"wait": 12}); err != nil {
		t.Fatal(err)
	}

	r, _, _ := s.Create(Proposal{Action: "mig"}, "admin")
	if _, err := s.Decide(r.ID, false, "admin", "not now"); err != nil {
		t.Fatal(err)
	}
	f, _, _ := s.Create(Proposal{Action: "nodes"}, "admin")
	s.Decide(f.ID, true, "admin", "")
	if got, _ := s.Complete(f.ID, executor.Result{OK: false, Error: "forbidden"}, "admin"); got.Status != Failed {
		t.Fatalf("failed execution status %s", got.Status)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 3 {
		t.Fatalf("reopened has %d proposals", len(reopened.List()))
	}
	got, _ := reopened.Get(p.ID)
	if got.Status != Executed || got.Actual["wait"] != 12 || got.DecidedBy != "admin" {
		t.Fatalf("reopened proposal %+v", got)
	}
	if n := len(reopened.Audit()); n != 8 {
		t.Fatalf("audit has %d events, want 8", n)
	}
	if _, err := reopened.Get("nope"); err != ErrNotFound {
		t.Fatalf("missing proposal err = %v", err)
	}
}
