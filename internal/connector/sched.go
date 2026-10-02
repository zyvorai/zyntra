// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

const (
	defaultInterval  = 5 * time.Minute
	defaultFiles     = time.Minute
	defaultTimeout   = time.Minute
	maxBackoffFactor = 8
	maxErrorLen      = 300
)

// Status is what the console shows about one job.
type Status struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Interval    string    `json:"interval"`
	LastRun     time.Time `json:"last_run,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	// LastStart is when the last successful run began: the cursor handed to
	// the next incremental pull as "since".
	LastStart   time.Time `json:"last_start,omitempty"`
	NextRun     time.Time `json:"next_run,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastObjects int       `json:"last_objects"`
	LastLinks   int       `json:"last_links"`
	LastSkipped int       `json:"last_skipped"`
	DurationMS  int64     `json:"duration_ms"`
	Runs        int       `json:"runs"`
	Failures    int       `json:"failures"`
	// Streak counts consecutive failures; it stretches the interval.
	Streak  int  `json:"streak"`
	Running bool `json:"running"`
	// Healthy is true when the last run succeeded and is recent enough
	// (within three intervals).
	Healthy bool `json:"healthy"`
}

type job struct {
	name     string
	kind     string
	interval time.Duration
	timeout  time.Duration
	run      func(ctx context.Context, since time.Time) (ontology.IngestReport, error)

	mu sync.Mutex
	st Status
}

// Scheduler runs a pack's file mappings and connectors on their intervals.
// Failures back off (up to 8x the interval) and are reported, never fatal.
type Scheduler struct {
	store *ontology.Store
	jobs  map[string]*job
	order []string
	by    string
	path  string
	now   func() time.Time

	saveMu sync.Mutex
}

// NewScheduler builds the jobs for a definition. statePath, when set, keeps
// each job's cursor and history across restarts.
func NewScheduler(st *ontology.Store, def *ontology.Definition, dir string, load ontology.Loader, opt Options, statePath string) (*Scheduler, error) {
	s := &Scheduler{store: st, jobs: map[string]*job{}, by: "zyntra (scheduler)", path: statePath, now: time.Now}
	if len(def.Mappings) > 0 {
		iv := defaultFiles
		if def.RefreshInterval != "" {
			iv, _ = time.ParseDuration(def.RefreshInterval)
		}
		s.add(&job{name: ontology.PackFilesJob, kind: "files", interval: iv, timeout: defaultTimeout,
			run: func(ctx context.Context, _ time.Time) (ontology.IngestReport, error) {
				reps, err := st.IngestMappings(def, dir, s.by, load, s.now().UTC())
				var total ontology.IngestReport
				for _, r := range reps {
					total.Objects += r.Objects
					total.Links += r.Links
					total.Candidates += r.Candidates
					total.Skipped = append(total.Skipped, r.Skipped...)
				}
				return total, err
			}})
	}
	for _, spec := range def.Connectors {
		c, err := FromSpec(spec, def.Schema(), opt)
		if err != nil {
			return nil, err
		}
		iv, to := defaultInterval, defaultTimeout
		if spec.Interval != "" {
			iv, _ = time.ParseDuration(spec.Interval)
		}
		if spec.Timeout != "" {
			to, _ = time.ParseDuration(spec.Timeout)
		}
		conn := c
		s.add(&job{name: spec.Name, kind: spec.Kind, interval: iv, timeout: to,
			run: func(ctx context.Context, since time.Time) (ontology.IngestReport, error) {
				return Run(ctx, st, conn, since, s.by, s.now().UTC())
			}})
	}
	s.restore()
	return s, nil
}

func (s *Scheduler) add(j *job) {
	j.st = Status{Name: j.name, Kind: j.kind, Interval: j.interval.String()}
	s.jobs[j.name] = j
	s.order = append(s.order, j.name)
}

// Statuses returns every job's status, in declaration order.
func (s *Scheduler) Statuses() []Status {
	out := make([]Status, 0, len(s.order))
	for _, n := range s.order {
		j := s.jobs[n]
		j.mu.Lock()
		st := j.st
		st.Healthy = st.LastError == "" && !st.LastSuccess.IsZero() && s.now().Sub(st.LastSuccess) < 3*j.interval
		j.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// RunNow runs one job immediately. It refuses if the job is already running.
func (s *Scheduler) RunNow(ctx context.Context, name string) (ontology.IngestReport, error) {
	j, ok := s.jobs[name]
	if !ok {
		return ontology.IngestReport{}, fmt.Errorf("no connector %q", name)
	}
	return s.execute(ctx, j)
}

// RunAll runs every job once, in order, and joins their errors.
func (s *Scheduler) RunAll(ctx context.Context) ([]ontology.IngestReport, error) {
	var reps []ontology.IngestReport
	var errs []error
	for _, n := range s.order {
		rep, err := s.execute(ctx, s.jobs[n])
		reps = append(reps, rep)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", n, err))
		}
	}
	return reps, errors.Join(errs...)
}

func (s *Scheduler) execute(ctx context.Context, j *job) (ontology.IngestReport, error) {
	j.mu.Lock()
	if j.st.Running {
		j.mu.Unlock()
		return ontology.IngestReport{}, errors.New("already running")
	}
	j.st.Running = true
	since := j.st.LastStart
	j.mu.Unlock()

	start := s.now()
	cctx, cancel := context.WithTimeout(ctx, j.timeout)
	rep, err := j.run(cctx, since)
	cancel()

	j.mu.Lock()
	j.st.Running = false
	j.st.Runs++
	j.st.LastRun = start
	j.st.DurationMS = s.now().Sub(start).Milliseconds()
	j.st.LastObjects, j.st.LastLinks, j.st.LastSkipped = rep.Objects, rep.Links, len(rep.Skipped)
	if err != nil {
		j.st.Failures++
		j.st.Streak++
		j.st.LastError = clip(err.Error())
	} else {
		j.st.Streak = 0
		j.st.LastError = ""
		j.st.LastSuccess = s.now()
		j.st.LastStart = start
	}
	j.st.NextRun = start.Add(j.delay())
	j.mu.Unlock()
	s.persist()
	return rep, err
}

// delay is the wait before the next run: the interval, stretched by failures.
// The caller holds j.mu.
func (j *job) delay() time.Duration {
	f := 1 << min(j.st.Streak, 3) // 1,2,4,8
	if f > maxBackoffFactor {
		f = maxBackoffFactor
	}
	return j.interval * time.Duration(f)
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrorLen {
		return s[:maxErrorLen] + "…"
	}
	return s
}

// Run schedules every job until ctx ends. Each job waits out its interval
// (with 10% jitter, so connectors do not all fire together) from its last run.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, n := range s.order {
		j := s.jobs[n]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j.mu.Lock()
				next := j.st.NextRun
				if next.IsZero() {
					next = s.now().Add(j.interval)
				}
				j.mu.Unlock()
				wait := time.Until(next)
				wait += time.Duration(rand.Float64() * 0.1 * float64(j.interval))
				if wait < time.Second {
					wait = time.Second
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
				_, _ = s.execute(ctx, j)
			}
		}()
	}
	wg.Wait()
}

type saved struct {
	Jobs map[string]Status `json:"jobs"`
}

func (s *Scheduler) persist() {
	if s.path == "" {
		return
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	out := saved{Jobs: map[string]Status{}}
	for _, st := range s.Statuses() {
		st.Running = false
		out.Jobs[st.Name] = st
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(s.path), 0o750) != nil {
		return
	}
	if os.WriteFile(s.path+".tmp", b, 0o640) == nil {
		_ = os.Rename(s.path+".tmp", s.path)
	}
}

func (s *Scheduler) restore() {
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var in saved
	if json.Unmarshal(b, &in) != nil {
		return
	}
	names := make([]string, 0, len(in.Jobs))
	for n := range in.Jobs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if j, ok := s.jobs[n]; ok {
			old := in.Jobs[n]
			j.st = old
			j.st.Name, j.st.Kind, j.st.Interval, j.st.Running = j.name, j.kind, j.interval.String(), false
		}
	}
}
