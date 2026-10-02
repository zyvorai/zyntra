// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package decisions

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/approvals"
)

func TestSignVerifyAndPersistentKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "decision.key")
	s, err := LoadSigner(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadSigner(path)
	if err != nil || again.PublicKey() != s.PublicKey() {
		t.Fatalf("key not persisted: %v", err)
	}
	st, _ := approvals.Open("")
	p, _, _ := st.Create(approvals.Proposal{Action: "spot", ActionName: "Spot"}, "alice")
	e, err := s.Sign(p, st.AuditFor(p.ID), st.Verify(), "auditor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(e)
	var back Export
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if err := Verify(back); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	back.Decision.CreatedBy = "mallory"
	if Verify(back) == nil {
		t.Fatal("tampered export verified")
	}
}
