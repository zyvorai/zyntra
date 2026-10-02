// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package approvals

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/outcome"
)

func fill(t *testing.T, s *Store) {
	t.Helper()
	p, _, _ := s.Create(Proposal{Action: "spot"}, "alice")
	if _, err := s.Decide(p.ID, true, "bob", "ok"); err != nil {
		t.Fatal(err)
	}
	if err := s.Note("manual", "carol", "entered a value"); err != nil {
		t.Fatal(err)
	}
}

func TestAuditIsAppendOnlyAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	fill(t, s)
	first, _ := os.ReadFile(path + ".audit.jsonl")
	if n := strings.Count(string(first), "\n"); n != 3 {
		t.Fatalf("want 3 lines, got %d:\n%s", n, first)
	}
	// More events only add lines; the bytes already written never change.
	_ = s.Note("manual", "dave", "another")
	second, _ := os.ReadFile(path + ".audit.jsonl")
	if !strings.HasPrefix(string(second), string(first)) || strings.Count(string(second), "\n") != 4 {
		t.Fatal("the audit file was rewritten rather than appended to")
	}
	state, _ := os.ReadFile(path)
	if strings.Contains(string(state), `"prev_hash"`) || strings.Contains(string(state), "entered a value") {
		t.Error("the state file still carries the audit trail")
	}
	want := s.Verify()
	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.Verify(); !got.OK || got.Events != 4 || got.Head != want.Head {
		t.Fatalf("after reopen: %+v want %+v", got, want)
	}
	// New events chain onto the reopened trail.
	_ = re.Note("manual", "erin", "after restart")
	if v := re.Verify(); !v.OK || v.Events != 5 {
		t.Fatalf("%+v", v)
	}
	again, _ := Open(path)
	if v := again.Verify(); !v.OK || v.Events != 5 {
		t.Fatalf("%+v", v)
	}
}

func TestLegacyInlineAuditMovesToTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	// Build a current-version state, then rewrite it the way the previous
	// release stored it: the audit trail inline in the one file.
	s, _ := Open("")
	fill(t, s)
	legacy := s.s
	legacy.AuditFile = false
	b, _ := jsonIndent(legacy)
	if err := os.WriteFile(path, b, 0o640); err != nil {
		t.Fatal(err)
	}
	want := s.Verify()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Verify(); !got.OK || got.Head != want.Head || got.Events != want.Events {
		t.Fatalf("migration changed the chain: %+v want %+v", got, want)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "entered a value") {
		t.Error("the inline trail was not moved out of the state file")
	}
	if _, err := os.Stat(path + ".audit.jsonl"); err != nil {
		t.Fatal("no audit file written")
	}
	re, _ := Open(path)
	if v := re.Verify(); !v.OK || v.Head != want.Head {
		t.Fatalf("after a second open: %+v", v)
	}
	// Both an inline trail and an audit file is ambiguous: refuse rather than guess.
	if err := os.WriteFile(path, b, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "refusing to guess") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestTornFinalLineIsToleratedButDamageIsNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	fill(t, s)
	f, _ := os.OpenFile(path+".audit.jsonl", os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"seq":4,"at":"2026-10-0`) // a crash in the middle of an append
	f.Close()
	r, err := Open(path)
	if err != nil {
		t.Fatalf("a torn final line must not stop start-up: %v", err)
	}
	if v := r.Verify(); !v.OK || v.Events != 3 {
		t.Fatalf("%+v", v)
	}
	// The unfinished line was cut, so the next event starts on a fresh line
	// and the whole trail reads back and verifies.
	if err := r.Note("manual", "x", "after the crash"); err != nil {
		t.Fatal(err)
	}
	after, err := Open(path)
	if err != nil {
		t.Fatalf("the trail did not survive a torn tail plus a new event: %v", err)
	}
	if v := after.Verify(); !v.OK || v.Events != 4 {
		t.Fatalf("%+v", v)
	}
	// A complete event whose newline never landed is kept, not dropped.
	raw0, _ := os.ReadFile(path + ".audit.jsonl")
	os.WriteFile(path+".audit.jsonl", []byte(strings.TrimRight(string(raw0), "\n")), 0o640)
	kept, err := Open(path)
	if err != nil || len(kept.Audit()) != 4 {
		t.Fatalf("a final event without its newline: %v, %d events", err, len(kept.Audit()))
	}
	_ = kept.Note("manual", "y", "next")
	if again, err := Open(path); err != nil || again.Verify().Events != 5 || !again.Verify().OK {
		t.Fatalf("append after a missing newline: %v %+v", err, again.Verify())
	}
	// Damage in the middle is tampering, not a crash.
	raw, _ := os.ReadFile(path + ".audit.jsonl")
	lines := strings.Split(string(raw), "\n")
	lines[1] = "not json"
	os.WriteFile(path+".audit.jsonl", []byte(strings.Join(lines, "\n")), 0o640)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("want a damaged-line error, got %v", err)
	}
}

func TestExplanationHashIsPersistedInTheChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, _ := Open(path)
	p, _, _ := s.Create(Proposal{Action: "spot"}, "alice")
	_, _ = s.Decide(p.ID, true, "bob", "")
	if _, err := s.Explain(p.ID, "zyntra", outcome.Explanation{Hash: "abc123", Text: "t"}); err != nil {
		t.Fatal(err)
	}
	re, _ := Open(path)
	ev := re.AuditFor(p.ID)
	if last := ev[len(ev)-1]; last.Explanation != "abc123" {
		t.Fatalf("explanation hash lost: %+v", last)
	}
	if v := re.Verify(); !v.OK {
		t.Fatalf("%+v", v)
	}
}

func jsonIndent(s state) ([]byte, error) { return json.MarshalIndent(s, "", "  ") }
