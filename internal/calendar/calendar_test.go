// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package calendar

import (
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skip(err)
	}
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, ist)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	set := Set{
		"buy":   {Days: []string{"mon", "tue", "wed", "thu", "fri", "saturday"}, Start: "10:00", End: "17:00"},
		"night": {Days: []string{"fri"}, Start: "22:00", End: "06:00"},
		"utc":   {Start: "00:00", End: "01:00", TZ: "UTC"},
	}
	if err := set.Compile(ist); err != nil {
		t.Fatal(err)
	}
	// 2026-10-02 is a Friday.
	cases := []struct {
		name string
		at   string
		want bool
	}{
		{"buy", "2026-10-02 10:00", true},
		{"buy", "2026-10-02 17:00", false},
		{"buy", "2026-10-04 12:00", false}, // Sunday
		{"buy", "2026-10-03 12:00", true},  // "saturday" matches by prefix
		{"night", "2026-10-02 23:30", true},
		{"night", "2026-10-03 05:59", true}, // early Saturday belongs to Friday night
		{"night", "2026-10-03 23:30", false},
		{"utc", "2026-10-02 05:45", true}, // 00:15 UTC
		{"utc", "2026-10-02 07:00", false},
	}
	for _, c := range cases {
		if got := set[c.name].Contains(at(c.at)); got != c.want {
			t.Errorf("%s at %s = %v, want %v", c.name, c.at, got, c.want)
		}
	}
	if !set.Any(nil, at("2026-10-04 03:00")) {
		t.Error("no names should allow any time")
	}
	if set.Any([]string{"missing"}, at("2026-10-02 12:00")) {
		t.Error("an unknown window must never match")
	}
	if (Window{Start: "10:00", End: "17:00"}).Contains(at("2026-10-02 12:00")) {
		t.Error("an uncompiled window must not match")
	}
}

func TestCompileErrors(t *testing.T) {
	for _, w := range []Window{
		{Start: "25:00", End: "06:00"},
		{Start: "10:00", End: "10:60"},
		{Start: "10", End: "11:00"},
		{Start: "10:00", End: "11:00", Days: []string{"funday"}},
		{Start: "10:00", End: "11:00", Days: []string{""}},
		{Start: "10:00", End: "11:00", TZ: "Mars/Olympus"},
	} {
		if err := w.Compile(nil); err == nil {
			t.Errorf("Compile(%+v) should fail", w)
		}
	}
}
