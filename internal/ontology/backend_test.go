// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func matchSchema() *Schema {
	s := testSchema()
	s.Objects[0].Match = "name"
	return s
}

// both runs a test against the JSON file and the SQLite backend.
func both(t *testing.T, f func(t *testing.T, path string)) {
	for _, ext := range []string{"json", "db"} {
		t.Run(ext, func(t *testing.T) { f(t, filepath.Join(t.TempDir(), "ont."+ext)) })
	}
}

func TestBackendsPersistEverything(t *testing.T) {
	both(t, func(t *testing.T, path string) {
		st, err := Open(path, matchSchema())
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		_, err = st.Ingest("erp", "t", []Record{
			{Type: "Cluster", Namespace: "erp", Key: "A", Props: map[string]any{"name": "GPU Cluster"}, Aliases: []Alias{{"erp", "A"}}},
			{Type: "Cluster", Namespace: "net", Key: "n1", Props: map[string]any{"name": "gpu-cluster"}},
			{Type: "Service", Namespace: "erp", Key: "s", Props: map[string]any{"name": "svc"},
				Links: []RecordLink{{Type: "runs_on", ToType: "Cluster", ToNS: "erp", ToKey: "A"}}},
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = st.Ingest("erp", "t", []Record{{Type: "Cluster", Namespace: "erp", Key: "A", Props: map[string]any{"gpus": 8.0}}}, now.Add(time.Hour))
		cand := st.Candidates()[0]
		if _, err := st.Decide(cand.ID, true, "alice"); err != nil {
			t.Fatal(err)
		}
		st.Close()

		re, err := Open(path, matchSchema())
		if err != nil {
			t.Fatal(err)
		}
		defer re.Close()
		if len(re.List("Cluster")) != 1 || len(re.List("Service")) != 1 {
			t.Fatalf("objects lost: %d clusters", len(re.List("Cluster")))
		}
		if got := re.Impact(cand.A, 0); len(got) != 1 {
			t.Fatalf("links lost or not rewired by the merge: %d", len(got))
		}
		if o, ok := re.Get(cand.B); !ok || o.ID != cand.A {
			t.Fatal("merge redirect lost")
		}
		if c := re.Candidates(); len(c) != 1 || c[0].Status != "accepted" {
			t.Fatalf("candidates = %+v", c)
		}
		h := re.History(cand.A)
		if len(h) != 2 {
			t.Fatalf("history lost: %d entries", len(h))
		}
		// Indexes are rebuilt on load: the alias still pins identity.
		rep, _ := re.Ingest("erp", "t", []Record{{Type: "Cluster", Namespace: "erp", Key: "A2", Aliases: []Alias{{"erp", "A"}}, Props: map[string]any{"gpus": 16.0}}}, now)
		if rep.Objects != 1 || len(re.List("Cluster")) != 1 {
			t.Fatalf("alias index not rebuilt: %+v clusters=%d", rep, len(re.List("Cluster")))
		}
	})
}

func TestSQLiteWritesOnlyWhatChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")
	st, _ := Open(path, testSchema())
	defer st.Close()
	var recs []Record
	for i := 0; i < 500; i++ {
		recs = append(recs, Record{Type: "Cluster", Namespace: "n", Key: fmt.Sprint(i), Props: map[string]any{"name": "c"}})
	}
	_, _ = st.Ingest("x", "t", recs, time.Now())
	be := st.backend.(*sqliteBackend)
	var before int
	_ = be.db.QueryRow("SELECT COUNT(*) FROM history").Scan(&before)
	// Re-ingesting identical data adds no history rows and changes no objects.
	_, _ = st.Ingest("x", "t", recs, time.Now().Add(time.Hour))
	var after int
	_ = be.db.QueryRow("SELECT COUNT(*) FROM history").Scan(&after)
	if before != 500 || after != before {
		t.Fatalf("history rows %d -> %d", before, after)
	}
	// A one-property change writes one history row.
	_ = st.Upsert(obj("Cluster", "k", map[string]any{"name": "d"}))
	var n int
	_ = be.db.QueryRow("SELECT COUNT(*) FROM history WHERE object=?", MakeID("Cluster", "lab", "k")).Scan(&n)
	if n != 1 {
		t.Fatalf("rows for the new object: %d", n)
	}
	if st.s.History != nil {
		t.Error("the SQLite backend should not hold the change log in memory")
	}
}

func TestMigrateJSONToSQLiteAndBack(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.json")
	st, _ := Open(src, matchSchema())
	_, _ = st.Ingest("erp", "t", []Record{
		{Type: "Cluster", Namespace: "x", Key: "c", Props: map[string]any{"name": "One"}},
		{Type: "Service", Namespace: "x", Key: "s", Props: map[string]any{"name": "S"}, Links: []RecordLink{{Type: "runs_on", ToType: "Cluster", ToNS: "x", ToKey: "c"}}},
	}, time.Now())
	_ = st.Upsert(obj("Cluster", "z", map[string]any{"name": "Two"}))
	st.Close()
	db := filepath.Join(dir, "b.db")
	o, l, err := Migrate(src, db, matchSchema())
	if err != nil || o != 3 || l != 1 {
		t.Fatalf("migrate = %d %d %v", o, l, err)
	}
	if _, _, err := Migrate(src, db, matchSchema()); err == nil {
		t.Error("migrating into a populated store must be refused")
	}
	back := filepath.Join(dir, "c.json")
	if o, _, err := Migrate(db, back, matchSchema()); err != nil || o != 3 {
		t.Fatalf("back = %d %v", o, err)
	}
	re, _ := Open(back, matchSchema())
	defer re.Close()
	if len(re.List("")) != 3 || len(re.History(MakeID("Cluster", "lab", "z"))) != 1 {
		t.Fatalf("round trip lost data: %d objects", len(re.List("")))
	}
	if _, _, err := Migrate(src, src, matchSchema()); err == nil {
		t.Error("same file accepted")
	}
}

func TestIndexedLookupsMatchScans(t *testing.T) {
	st, _ := Open("", matchSchema())
	var recs []Record
	for i := 0; i < 300; i++ {
		recs = append(recs, Record{Type: "Cluster", Namespace: "n", Key: fmt.Sprint(i), Aliases: []Alias{{"sys", fmt.Sprint(i)}},
			Props: map[string]any{"name": fmt.Sprintf("Cluster %d", i%100)}}) // three share each name
	}
	rep, _ := st.Ingest("x", "t", recs, time.Now())
	if len(st.List("Cluster")) != 300 || rep.Candidates == 0 {
		t.Fatalf("objects=%d candidates=%d", len(st.List("Cluster")), rep.Candidates)
	}
	// Three objects per name give three candidate pairs each.
	if got := len(st.Candidates()); got != 300 {
		t.Fatalf("want 100 names x 3 pairs, got %d", got)
	}
	if id, ok := st.byAlias([]Alias{{"sys", "42"}}, ""); !ok || id != MakeID("Cluster", "n", "42") {
		t.Fatalf("alias lookup = %q %v", id, ok)
	}
	if _, ok := st.byAlias([]Alias{{"sys", "42"}}, "other"); ok {
		t.Error("an alias matched across tenants")
	}
}

func BenchmarkIngest(b *testing.B) {
	for _, ext := range []string{"json", "db"} {
		b.Run(ext, func(b *testing.B) {
			s := testSchema()
			for i := 0; i < b.N; i++ {
				st, _ := Open(filepath.Join(b.TempDir(), "o."+ext), s)
				var recs []Record
				for n := 0; n < 5000; n++ {
					recs = append(recs, Record{Type: "Cluster", Namespace: "n", Key: fmt.Sprint(n), Props: map[string]any{"name": "c"}})
				}
				_, _ = st.Ingest("x", "t", recs, time.Now())
				st.Close()
			}
		})
	}
}
