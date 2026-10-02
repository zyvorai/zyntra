// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package approvals

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/executor"
	"github.com/zyvorai/zyntra/internal/policy"
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

func TestQuorumDistinctAndExpiry(t *testing.T) {
	s, _ := Open("")
	now := time.Unix(1_800_000_000, 0).UTC()
	s.now = func() time.Time { return now }
	pol := &policy.Effective{Approvals: 2, DistinctFromProposer: true, PendingExpiry: time.Hour, ApprovedExpiry: 10 * time.Minute}
	p, _, _ := s.Create(Proposal{Action: "spot", RequiredApprovals: 2, Policy: pol}, "alice")
	if p.ExpiresAt == nil || !p.ExpiresAt.Equal(now.Add(time.Hour)) || p.Phase != PhaseProposed {
		t.Fatalf("created %+v", p)
	}
	if _, _, err := s.Approve(p.ID, Approval{By: "alice"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("proposer approved: %v", err)
	}
	p, done, err := s.Approve(p.ID, Approval{By: "bob", Role: "approver", Reason: "ok"})
	if err != nil || done || p.Status != Pending || len(p.Approvals) != 1 {
		t.Fatalf("first approval %+v %v %v", p, done, err)
	}
	if _, _, err := s.Approve(p.ID, Approval{By: "bob"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same approver twice: %v", err)
	}
	p, done, err = s.Approve(p.ID, Approval{By: "carol", Role: "admin"})
	if err != nil || !done || p.Status != Approved || p.Phase != PhaseApproved || !p.ExpiresAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("quorum %+v %v %v", p, done, err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := s.Begin(p.ID); err == nil {
		t.Fatal("began an expired approval")
	}
	if got, _ := s.Get(p.ID); got.Status != Expired || got.Phase != PhaseExpired {
		t.Fatalf("after expiry %+v", got)
	}

	q, _, _ := s.Create(Proposal{Action: "mig", Policy: pol}, "alice")
	now = now.Add(2 * time.Hour)
	if ids, _ := s.ExpireDue(); len(ids) != 1 || ids[0] != q.ID {
		t.Fatalf("expire due %v", ids)
	}
	if v := s.Verify(); !v.OK || v.Events != 6 {
		t.Fatalf("verify %+v", v)
	}
}

func TestBlockAndAuditTamper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	p, _, _ := s.Create(Proposal{Action: "spot"}, "admin")
	s.Decide(p.ID, true, "admin", "")
	b, err := s.Block(p.ID, Revalidation{Reasons: []string{"model changed"}}, "zyntra")
	if err != nil || b.Status != Blocked || b.Phase != PhaseBlocked || len(b.BlockedReasons) != 1 {
		t.Fatalf("block %+v %v", b, err)
	}
	if _, err := s.Complete(p.ID, executor.Result{OK: true}, "x"); err == nil {
		t.Fatal("completed a blocked proposal")
	}
	if v := s.Verify(); !v.OK || v.Head == "" {
		t.Fatalf("verify %+v", v)
	}
	// The audit trail lives in its own append-only file; that is what an
	// attacker would edit.
	raw, _ := os.ReadFile(path + ".audit.jsonl")
	tampered := strings.Replace(string(raw), `"to":"approved"`, `"to":"rejected"`, 1)
	if tampered == string(raw) {
		t.Fatal("test bug: nothing was tampered")
	}
	os.WriteFile(path+".audit.jsonl", []byte(tampered), 0o600)
	r, _ := Open(path)
	if v := r.Verify(); v.OK || v.BrokenAt != 2 {
		t.Fatalf("tampered chain verified: %+v", v)
	}
}

func TestMigratesV1State(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v1 := `{"proposals":[{"id":"prop-1","action":"spot","status":"executed","decided_by":"admin","decided_at":"2026-01-01T00:00:00Z","execution":{"mode":"dry-run","ok":true}}],
"audit":[{"at":"2026-01-01T00:00:00Z","proposal":"prop-1","action":"spot","to":"pending","by":"admin"},{"at":"2026-01-01T00:00:01Z","proposal":"prop-1","action":"spot","from":"pending","to":"approved","by":"admin"}]}`
	os.WriteFile(path, []byte(v1), 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.Get("prop-1")
	if p.Phase != PhaseDryRunValidated || len(p.Approvals) != 1 || p.Approvals[0].By != "admin" {
		t.Fatalf("migrated %+v", p)
	}
	if v := s.Verify(); !v.OK || !v.Migrated || v.Events != 2 {
		t.Fatalf("verify %+v", v)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"version": 2`) {
		t.Fatal("migration not persisted")
	}
}

func TestAuditCoversPayloadAndResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	p, _, _ := s.Create(Proposal{Action: "markdown", Render: "POST ${ZYNTRA_POS_URL}/markdowns\n{\"percent\":10}"}, "admin")
	s.Decide(p.ID, true, "admin", "")
	res := executor.Result{OK: true, Kind: "webhook", PayloadHash: strings.Repeat("a", 64), ResponseHash: strings.Repeat("b", 64)}
	if _, err := s.Complete(p.ID, res, "zyntra"); err != nil {
		t.Fatal(err)
	}
	events := s.AuditFor(p.ID)
	if events[0].Payload == "" || events[0].Payload != renderHash(p.Render) {
		t.Fatalf("proposal event must carry the rendered payload hash: %+v", events[0])
	}
	last := events[len(events)-1]
	if last.Payload != res.PayloadHash || last.Response != res.ResponseHash {
		t.Fatalf("execution event hashes: %+v", last)
	}
	if v := s.Verify(); !v.OK {
		t.Fatalf("verify %+v", v)
	}
	raw, _ := os.ReadFile(path + ".audit.jsonl")
	changed := strings.ReplaceAll(string(raw), strings.Repeat("b", 64), strings.Repeat("c", 64))
	if changed == string(raw) {
		t.Fatal("test bug: nothing was tampered")
	}
	os.WriteFile(path+".audit.jsonl", []byte(changed), 0o600)
	r, _ := Open(path)
	if v := r.Verify(); v.OK {
		t.Fatal("changing the recorded response hash must break the chain")
	}
}
