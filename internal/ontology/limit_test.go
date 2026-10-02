// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clusters(from, to int) []Record {
	var out []Record
	for i := from; i < to; i++ {
		out = append(out, Record{Type: "Cluster", Namespace: "n", Key: fmt.Sprint(i), Props: map[string]any{"name": fmt.Sprint("c", i)}})
	}
	return out
}

func TestObjectLimitRefusesTheWholeBatch(t *testing.T) {
	both(t, func(t *testing.T, path string) {
		st, _ := Open(path, testSchema())
		defer st.Close()
		st.SetMaxObjects(10)
		now := time.Now()
		if _, err := st.Ingest("x", "t", clusters(0, 8), now); err != nil {
			t.Fatal(err)
		}
		// 5 new objects would make 13: nothing from the batch is written, not even the 2 that would fit.
		_, err := st.Ingest("x", "t", clusters(8, 13), now)
		if !errors.Is(err, ErrObjectLimit) || !strings.Contains(err.Error(), "limit is 10") || !strings.Contains(err.Error(), "ZYNTRA_ONTOLOGY_MAX_OBJECTS") {
			t.Fatalf("want a clear limit error, got %v", err)
		}
		if got := len(st.List("")); got != 8 {
			t.Fatalf("a refused batch wrote %d objects", got-8)
		}
		// Updating objects that already exist adds nothing, so it is fine at the cap.
		if _, err := st.Ingest("x", "t", clusters(0, 8), now.Add(time.Minute)); err != nil {
			t.Fatalf("an update within the cap was refused: %v", err)
		}
		// A batch that fits exactly is accepted; one more is not.
		if _, err := st.Ingest("x", "t", clusters(8, 10), now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Ingest("x", "t", clusters(10, 11), now); !errors.Is(err, ErrObjectLimit) {
			t.Fatalf("the 11th object was accepted: %v", err)
		}
		// Lifting the cap lets it through.
		st.SetMaxObjects(0)
		if _, err := st.Ingest("x", "t", clusters(10, 50), now); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStats(t *testing.T) {
	st, _ := Open("", matchSchema())
	st.SetMaxObjects(100)
	_, _ = st.Ingest("x", "t", []Record{
		{Type: "Cluster", Namespace: "a", Key: "1", Props: map[string]any{"name": "Main"}},
		{Type: "Cluster", Namespace: "b", Key: "1", Props: map[string]any{"name": "main"}},
		{Type: "Service", Namespace: "a", Key: "s", Props: map[string]any{"name": "s"}, Links: []RecordLink{{Type: "runs_on", ToType: "Cluster", ToNS: "a", ToKey: "1"}}},
	}, time.Now())
	got := st.Stats()
	if got.Objects != 3 || got.Links != 1 || got.Candidates != 1 || got.ByType["Cluster"] != 2 || got.Limit != 100 {
		t.Fatalf("%+v", got)
	}
}

// Trimming the in-memory change log on every change once it was full made a
// large ingest quadratic (100k objects took 98 s). It must stay linear, and
// the log must stay bounded.
func TestChangeLogTrimIsAmortisedAndBounded(t *testing.T) {
	st, _ := Open("", podSchema())
	start := time.Now()
	for off := 0; off < 60000; off += 5000 {
		var recs []Record
		for i := off; i < off+5000; i++ {
			recs = append(recs, Record{Type: "Workload", Namespace: "k", Key: fmt.Sprint(i), Props: map[string]any{"name": fmt.Sprint(i), "namespace": "d", "phase": "Running"}})
		}
		if _, err := st.Ingest("k", "t", recs, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	took := time.Since(start)
	if limit := 15 * time.Second * slowdown; took > limit {
		t.Fatalf("180k changes took %v: the change log trim is not amortised", took)
	}
	if n := len(st.s.History); n < maxHistory || n >= 2*maxHistory {
		t.Fatalf("change log holds %d entries, want between %d and %d", n, maxHistory, 2*maxHistory-1)
	}
	// The newest changes are the ones kept.
	last := st.s.History[len(st.s.History)-1]
	if !strings.HasPrefix(last.Object, "Workload:k:") {
		t.Errorf("last change = %+v", last)
	}
	_ = filepath.Join
}
