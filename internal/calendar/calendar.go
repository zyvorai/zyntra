// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package calendar holds named weekly windows such as shop hours, a shift or
// a change window. Windows gate when an approved action may run and, for
// KPIs that name one, which samples count.
package calendar

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window is a recurring weekly window. End before Start wraps past
// midnight. An empty Days list means every day.
type Window struct {
	Days  []string `yaml:"days,omitempty" json:"days,omitempty"`
	Start string   `yaml:"start" json:"start"`
	End   string   `yaml:"end" json:"end"`
	TZ    string   `yaml:"timezone,omitempty" json:"timezone,omitempty"`

	loc        *time.Location
	start, end int
	days       map[time.Weekday]bool
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

func clock(s string) (int, error) {
	h, m, ok := strings.Cut(s, ":")
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if !ok || err1 != nil || err2 != nil || hh < 0 || hh > 24 || mm < 0 || mm > 59 || hh*60+mm > 24*60 {
		return 0, fmt.Errorf("bad time %q (want HH:MM)", s)
	}
	return hh*60 + mm, nil
}

// Compile parses the window. Windows without their own timezone use def
// (UTC when def is nil).
func (w *Window) Compile(def *time.Location) error {
	var err error
	if w.start, err = clock(w.Start); err != nil {
		return err
	}
	if w.end, err = clock(w.End); err != nil {
		return err
	}
	w.loc = def
	if w.loc == nil {
		w.loc = time.UTC
	}
	if w.TZ != "" {
		if w.loc, err = time.LoadLocation(w.TZ); err != nil {
			return err
		}
	}
	w.days = map[time.Weekday]bool{}
	for _, d := range w.Days {
		wd, ok := weekdays[strings.ToLower(d)[:min(3, len(d))]]
		if !ok {
			return fmt.Errorf("unknown day %q", d)
		}
		w.days[wd] = true
	}
	return nil
}

// Compiled reports whether Compile has run.
func (w Window) Compiled() bool { return w.loc != nil }

// Contains reports whether t falls inside the window.
func (w Window) Contains(t time.Time) bool {
	if w.loc == nil {
		return false
	}
	lt := t.In(w.loc)
	mins := lt.Hour()*60 + lt.Minute()
	day := lt.Weekday()
	if w.start <= w.end {
		return w.dayOK(day) && mins >= w.start && mins < w.end
	}
	// Wraps midnight: the late part belongs to day, the early part to the
	// day before.
	if mins >= w.start {
		return w.dayOK(day)
	}
	return mins < w.end && w.dayOK((day+6)%7)
}

func (w Window) dayOK(d time.Weekday) bool { return len(w.days) == 0 || w.days[d] }

// Set is a collection of named windows.
type Set map[string]Window

// Compile parses every window in place.
func (s Set) Compile(def *time.Location) error {
	for name, w := range s {
		if err := w.Compile(def); err != nil {
			return fmt.Errorf("window %s: %w", name, err)
		}
		s[name] = w
	}
	return nil
}

// Any reports whether t falls in any of the named windows. With no names
// every time is allowed; unknown names never match.
func (s Set) Any(names []string, t time.Time) bool {
	if len(names) == 0 {
		return true
	}
	for _, n := range names {
		if w, ok := s[n]; ok && w.Contains(t) {
			return true
		}
	}
	return false
}
