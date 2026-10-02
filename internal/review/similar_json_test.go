// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/approvals"
)

// A decision with no precedent must serialise "items" as [], not null: the
// console reads its length, and a null list blanked the whole Decision page.
func TestSimilarSerialisesAnEmptyListNotNull(t *testing.T) {
	got := Similar(nil, approvals.Proposal{ID: "p1", Action: "a"}, 3)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("items must be an empty list, got %s", b)
	}
	// Only itself on record: still none.
	only := approvals.Proposal{ID: "p1", Action: "a"}
	if b, _ := json.Marshal(Similar([]approvals.Proposal{only}, only, 3)); !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("%s", b)
	}
}
