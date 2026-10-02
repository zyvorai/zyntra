// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package actions

import (
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

const def = `
objects:
  - name: Service
    properties: [{name: name, type: string}, {name: tier, type: string}]
links: []
actions:
  - id: boost
    inputs: [{name: service, object_type: Service, required: true}]
    requires: [{input: service, property: tier, equals: inference}]
    evidence: [{input: service, properties: [tier], max_age: 1h}]
`

func setup(t *testing.T, observed time.Time) (*Registry, *ontology.Store, ontology.ObjectRef) {
	t.Helper()
	d, err := ontology.ParseDefinition([]byte(def))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	if _, err := st.Ingest("mes", "t", []ontology.Record{{Type: "Service", Namespace: "x", Key: "s1",
		Props: map[string]any{"name": "Infer", "tier": "inference"}, ObservedAt: observed}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return New(d, st, nil), st, ontology.ObjectRef{Input: "service", ID: "Service:x:s1", Type: "Service"}
}

func TestEvidenceAgeBlocksStaleFacts(t *testing.T) {
	admin := func(string) bool { return true }
	p := ontology.Principal{Roles: []string{"admin"}}
	r, _, _ := setup(t, time.Now().Add(-10*time.Minute))
	if _, problems := r.Validate("boost", map[string]string{"service": "Service:x:s1"}, p, admin); len(problems) != 0 {
		t.Fatalf("fresh evidence refused: %v", problems)
	}
	r, _, _ = setup(t, time.Now().Add(-3*time.Hour))
	_, problems := r.Validate("boost", map[string]string{"service": "Service:x:s1"}, p, admin)
	if len(problems) != 1 || !strings.Contains(problems[0], "stale") {
		t.Fatalf("want a stale-evidence problem, got %v", problems)
	}
}

func TestRecheckCatchesChangesAfterApproval(t *testing.T) {
	r, st, ref := setup(t, time.Now())
	refs := []ontology.ObjectRef{ref}
	digest := r.Digest("boost", refs)
	if problems := r.Recheck("boost", refs, digest); len(problems) != 0 {
		t.Fatalf("unchanged objects refused: %v", problems)
	}
	// The tier changes after approval.
	_, _ = st.Ingest("mes", "t", []ontology.Record{{Type: "Service", Namespace: "x", Key: "s1", Props: map[string]any{"tier": "batch"}}}, time.Now())
	problems := r.Recheck("boost", refs, digest)
	if len(problems) < 2 {
		t.Fatalf("want a digest mismatch and a failed requirement, got %v", problems)
	}
	// Time passing alone makes evidence stale.
	r2, _, ref2 := setup(t, time.Now())
	d2 := r2.Digest("boost", []ontology.ObjectRef{ref2})
	r2.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if p := r2.Recheck("boost", []ontology.ObjectRef{ref2}, d2); len(p) != 1 || !strings.Contains(p[0], "stale") {
		t.Fatalf("want stale evidence at execution time, got %v", p)
	}
	if p := r2.Recheck("gone", nil, ""); len(p) != 1 {
		t.Errorf("unknown action: %v", p)
	}
}

func TestEvidenceRuleValidation(t *testing.T) {
	bad := strings.Replace(def, "max_age: 1h", "max_age: soon", 1)
	if _, err := ontology.ParseDefinition([]byte(bad)); err == nil {
		t.Error("accepted a bad max_age")
	}
	bad = strings.Replace(def, "evidence: [{input: service", "evidence: [{input: nope", 1)
	if _, err := ontology.ParseDefinition([]byte(bad)); err == nil {
		t.Error("accepted evidence on an unknown input")
	}
}
