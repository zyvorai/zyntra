// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package rollout

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

func f(v float64) *float64 { return &v }

func plan() *ontology.Rollout {
	return &ontology.Rollout{Stages: []ontology.Stage{
		{Name: "canary", Sites: []string{"a"}, Gates: []ontology.Gate{{KPI: "wait", Max: f(30)}}},
		{Name: "fleet", Sites: []string{"b", "c"}, Gates: []ontology.Gate{{KPI: "avail", Min: f(99.9)}}},
	}}
}

type kpis map[string]float64

func (k kpis) value(id string) (float64, bool, bool) {
	v, ok := k[id]
	if !ok {
		return 0, false, false
	}
	if v < 0 {
		return 0, false, true // stale
	}
	return v, true, true
}

func TestHappyPathAdvancesStageByStage(t *testing.T) {
	s, _ := Open("")
	r, created, err := s.Create("d1", "act", "", plan())
	if err != nil || !created || r.Current != "canary" || r.Stages[1].State != StageWaiting {
		t.Fatalf("%+v %v %v", r, created, err)
	}
	if again, created, _ := s.Create("d1", "act", "", plan()); created || again.ID != "d1" {
		t.Error("create must be idempotent")
	}
	k := kpis{"wait": 12, "avail": 99.95}
	r, err = s.Report("d1", "canary", "a", SiteStarted, "", "deploy", k.value)
	if err != nil || r.Current != "canary" || r.Stages[0].State != StageRunning {
		t.Fatalf("started: %+v %v", r, err)
	}
	r, _ = s.Report("d1", "canary", "a", SiteHealthy, "ok", "deploy", k.value)
	if r.Current != "fleet" || r.Stages[0].State != StageHealthy || r.Stages[1].State != StageRunning || !r.Stages[0].GateCheck[0].OK {
		t.Fatalf("after canary: %+v", r)
	}
	r, _ = s.Report("d1", "fleet", "b", SiteHealthy, "", "deploy", k.value)
	if r.State != RolloutOpen || r.Current != "fleet" {
		t.Fatalf("one of two sites: %+v", r)
	}
	r, _ = s.Report("d1", "fleet", "c", SiteHealthy, "", "deploy", k.value)
	if r.State != RolloutComplete || r.Current != "" {
		t.Fatalf("complete: %+v", r)
	}
	if _, err := s.Report("d1", "fleet", "c", SiteHealthy, "", "deploy", k.value); !errors.Is(err, ErrFinished) {
		t.Errorf("a finished rollout accepted a report: %v", err)
	}
}

func TestCannotJumpAheadOrReportUnknowns(t *testing.T) {
	s, _ := Open("")
	_, _, _ = s.Create("d1", "act", "", plan())
	k := kpis{"wait": 1, "avail": 100}
	for name, c := range map[string]struct {
		stage, site, state string
		want               error
	}{
		"later stage":   {"fleet", "b", SiteHealthy, ErrNotCurrent},
		"unknown stage": {"nope", "a", SiteHealthy, ErrBadReport},
		"unknown site":  {"canary", "zzz", SiteHealthy, ErrBadReport},
		"bad state":     {"canary", "a", "great", ErrBadReport},
	} {
		if _, err := s.Report("d1", c.stage, c.site, c.state, "", "x", k.value); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Report("missing", "canary", "a", SiteHealthy, "", "x", k.value); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown rollout: %v", err)
	}
}

func TestGateBlocksAndRecovers(t *testing.T) {
	s, _ := Open("")
	_, _, _ = s.Create("d1", "act", "", plan())
	k := kpis{"wait": 55, "avail": 100} // over the 30 limit
	r, _ := s.Report("d1", "canary", "a", SiteHealthy, "", "deploy", k.value)
	if r.State != RolloutHalted || r.Stages[0].State != StageBlocked || r.Current != "canary" || r.Stages[1].State != StageWaiting {
		t.Fatalf("a failed gate must stop the rollout at the stage: %+v", r)
	}
	if g := r.Stages[0].GateCheck[0]; g.OK || g.Value == nil || *g.Value != 55 || g.Reason == "" {
		t.Errorf("gate check = %+v", g)
	}
	// The KPI recovers; a recheck lets the rollout continue without a new report.
	k["wait"] = 10
	r, _ = s.Recheck("d1", k.value)
	if r.State != RolloutOpen || r.Current != "fleet" {
		t.Fatalf("after recovery: %+v", r)
	}
}

func TestStaleOrUnknownGateDataFailsClosed(t *testing.T) {
	for name, k := range map[string]kpis{"stale": {"wait": -1, "avail": 100}, "unknown": {"avail": 100}} {
		s, _ := Open("")
		_, _, _ = s.Create("d1", "act", "", plan())
		r, _ := s.Report("d1", "canary", "a", SiteHealthy, "", "deploy", k.value)
		if r.State != RolloutHalted || r.Stages[0].State != StageBlocked {
			t.Errorf("%s gate data let the rollout continue: %+v", name, r)
		}
	}
}

func TestFailureHaltsAndCanBeRetried(t *testing.T) {
	s, _ := Open("")
	_, _, _ = s.Create("d1", "act", "", plan())
	k := kpis{"wait": 5, "avail": 100}
	r, _ := s.Report("d1", "canary", "a", SiteFailed, "image pull error", "deploy", k.value)
	if r.State != RolloutHalted || r.Stages[0].State != StageFailed || r.Stages[0].Reports["a"].Note != "image pull error" {
		t.Fatalf("%+v", r)
	}
	// The tool fixes the site and reports again.
	r, _ = s.Report("d1", "canary", "a", SiteHealthy, "", "deploy", k.value)
	if r.State != RolloutOpen || r.Current != "fleet" {
		t.Fatalf("retry: %+v", r)
	}
}

func TestAbortIsTerminalAndPersistenceWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	s, _ := Open(path)
	_, _, _ = s.Create("d1", "act", "alpha", plan())
	if _, err := s.Abort("d1", "customer asked"); err != nil {
		t.Fatal(err)
	}
	re, _ := Open(path)
	r, ok := re.Get("d1")
	if !ok || r.State != RolloutAborted || r.Tenant != "alpha" || r.Reason == "" {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, err := re.Report("d1", "canary", "a", SiteHealthy, "", "x", kpis{}.value); !errors.Is(err, ErrFinished) {
		t.Errorf("aborted rollout accepted a report: %v", err)
	}
	if _, err := re.Abort("d1", "again"); !errors.Is(err, ErrFinished) {
		t.Errorf("double abort: %v", err)
	}
	if _, _, err := re.Create("x", "a", "", &ontology.Rollout{}); err == nil {
		t.Error("an empty plan was accepted")
	}
	_ = time.Now
}
