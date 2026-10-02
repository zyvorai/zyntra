// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package envx

import (
	"reflect"
	"testing"
)

func TestExpand(t *testing.T) {
	t.Setenv("ZYNTRA_POS_URL", "http://pos.local")
	t.Setenv("ZYNTRA_API_KEY", "admin-secret")
	t.Setenv("HOME_SECRET", "nope")

	got, missing := Expand("${ZYNTRA_POS_URL}/sales?k=${ZYNTRA_API_KEY}&h=${HOME_SECRET}&u=${ZYNTRA_UNSET}")
	want := "http://pos.local/sales?k=${ZYNTRA_API_KEY}&h=${HOME_SECRET}&u=${ZYNTRA_UNSET}"
	if got != want {
		t.Fatalf("Expand = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(missing, []string{"ZYNTRA_API_KEY", "HOME_SECRET", "ZYNTRA_UNSET"}) {
		t.Fatalf("missing = %v", missing)
	}
}

func TestAllowedAndRefs(t *testing.T) {
	for name, want := range map[string]bool{
		"ZYNTRA_ERP_TOKEN":      true,
		"ZYNTRA_SESSION_SECRET": false,
		"ZYNTRA_INGEST_TOKEN":   false,
		"PATH":                  false,
	} {
		if Allowed(name) != want {
			t.Errorf("Allowed(%s) = %v", name, !want)
		}
	}
	if got := Refs("${B}/${A}", "${A}"); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("Refs = %v", got)
	}
}
