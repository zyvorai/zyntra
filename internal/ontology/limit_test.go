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

func ids(os []Object) []string {
	var out []string
	for _, o := range os {
		out = append(out, o.ID)
	}
	return out
}

func TestPageWalksEverythingOnceInOrder(t *testing.T) {
	st, _ := Open("", testSchema())
	_, _ = st.Ingest("x", "t", clusters(0, 23), time.Now())
	_, _ = st.Ingest("x", "t", []Record{{Type: "Service", Namespace: "n", Key: "s1", Props: map[string]any{"name": "s"}}}, time.Now())
	var all []string
	cursor := ""
	pages := 0
	for {
		items, next := st.Page("Cluster", cursor, 5, nil)
		all = append(all, ids(items)...)
		pages++
		if next == "" {
			break
		}
		if len(items) != 5 {
			t.Fatalf("a non-final page held %d items", len(items))
		}
		cursor = next
	}
	if len(all) != 23 || pages != 5 {
		t.Fatalf("%d items over %d pages", len(all), pages)
	}
	for i := 1; i < len(all); i++ {
		if all[i] <= all[i-1] {
			t.Fatalf("not in id order at %d: %s then %s", i, all[i-1], all[i])
		}
	}
	// A page that ends exactly on the last match has no next page.
	items, next := st.Page("Cluster", "", 23, nil)
	if len(items) != 23 || next != "" {
		t.Errorf("an exactly-full last page: %d items, next %q", len(items), next)
	}
	if items, next := st.Page("Nope", "", 5, nil); len(items) != 0 || next != "" {
		t.Errorf("unknown type: %d %q", len(items), next)
	}
	// The type filter skips the Service without counting it.
	if items, _ := st.Page("", "", 100, nil); len(items) != 24 {
		t.Errorf("all types: %d", len(items))
	}
}

func TestPageCursorSurvivesChangesBetweenRequests(t *testing.T) {
	st, _ := Open("", testSchema())
	_, _ = st.Ingest("x", "t", clusters(10, 20), time.Now())
	first, next := st.Page("Cluster", "", 4, nil)
	// Between the two requests: one object before the cursor is added, the
	// cursor object itself is deleted, and one after it is added.
	_, _ = st.Ingest("x", "t", clusters(0, 10), time.Now())
	_, _ = st.Ingest("x", "t", clusters(20, 25), time.Now())
	st.mu.Lock()
	st.delObject(next)
	st.mu.Unlock()
	rest := []string{}
	for cursor := next; ; {
		items, n := st.Page("Cluster", cursor, 4, nil)
		rest = append(rest, ids(items)...)
		if n == "" {
			break
		}
		cursor = n
	}
	seen := map[string]bool{}
	for _, id := range append(ids(first), rest...) {
		if seen[id] {
			t.Fatalf("%s was returned twice", id)
		}
		seen[id] = true
	}
	// Everything after the old cursor is still reached, including the new objects; none was skipped.
	for i := 11; i < 25; i++ {
		id := MakeID("Cluster", "n", fmt.Sprint(i))
		if id > next && id != next && !seen[id] {
			t.Errorf("%s was skipped", id)
		}
	}
}

func TestPageUsesTheCurrentSetAfterChanges(t *testing.T) {
	st, _ := Open("", testSchema())
	_, _ = st.Ingest("x", "t", clusters(0, 3), time.Now())
	if items, _ := st.Page("", "", 10, nil); len(items) != 3 {
		t.Fatal("setup")
	}
	_, _ = st.Ingest("x", "t", clusters(3, 5), time.Now()) // cache must be invalidated
	if items, _ := st.Page("", "", 10, nil); len(items) != 5 {
		t.Fatalf("a stale sorted cache: %d items", len(items))
	}
	st.mu.Lock()
	st.delObject(MakeID("Cluster", "n", "0"))
	st.mu.Unlock()
	if items, _ := st.Page("", "", 10, nil); len(items) != 4 {
		t.Fatalf("a deleted object is still listed: %d", len(items))
	}
	// An update of an existing object does not change the order, so the cache stays valid.
	v := st.version
	_, _ = st.Ingest("x", "t", clusters(1, 2), time.Now().Add(time.Hour))
	if st.version != v {
		t.Error("updating an existing object invalidated the sorted cache")
	}
}

func TestReaderPageSkipsHiddenObjectsWithoutShorteningPages(t *testing.T) {
	st, _ := Open("", testSchema())
	for i := 0; i < 12; i++ {
		tenant := "alpha"
		if i%2 == 1 {
			tenant = "beta"
		}
		_ = st.Upsert(Object{ID: MakeID("Cluster", "n", fmt.Sprintf("%02d", i)), Type: "Cluster", Tenant: tenant,
			Props: map[string]Value{"name": {V: fmt.Sprint("c", i), Prov: prov()}}})
	}
	rd := st.As(nil, Principal{Roles: []string{"approver"}, Tenant: "alpha"})
	var got []string
	for cursor := ""; ; {
		items, next := rd.Page("", cursor, 4, nil)
		if next != "" && len(items) != 4 {
			t.Fatalf("hidden objects shortened a page: %d", len(items))
		}
		got = append(got, ids(items)...)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != 6 {
		t.Fatalf("alpha sees %d, want 6: %v", len(got), got)
	}
	for _, id := range got {
		if o, _ := st.Get(id); o.Tenant != "alpha" {
			t.Errorf("leaked %s", id)
		}
	}
}

func BenchmarkPageOverALargeStore(b *testing.B) {
	st, _ := Open("", podSchema())
	for off := 0; off < 200000; off += 5000 {
		var recs []Record
		for i := off; i < off+5000; i++ {
			recs = append(recs, Record{Type: "Workload", Namespace: "k", Key: fmt.Sprintf("%07d", i), Props: map[string]any{"name": "p"}})
		}
		_, _ = st.Ingest("k", "t", recs, time.Now())
	}
	st.Page("", "", 1, nil) // build the sorted cache once
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		st.Page("", "", 200, nil)
	}
}
