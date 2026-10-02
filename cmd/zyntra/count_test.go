// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import "testing"

func TestParseCount(t *testing.T) {
	good := map[string]int{"0": 0, "250000": 250000, " 42 ": 42, "1e+06": 1000000, "1E6": 1000000, "2.5e5": 250000}
	for in, want := range good {
		if got, err := parseCount(in); err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "-1", "1.5", "1e-3", "1e99", "NaN", "Inf"} {
		if _, err := parseCount(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}
