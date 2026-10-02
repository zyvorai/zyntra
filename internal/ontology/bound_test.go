// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func boundSchema() *Schema {
	return &Schema{
		Objects: []ObjectType{
			{Name: "Node", KPIs: []string{"cpu"}, Properties: []Property{{Name: "name", Type: "string"}, {Name: "kpis", Type: "string"}}, Match: "name"},
			{Name: "Workload", Properties: []Property{{Name: "name", Type: "string"}, {Name: "kpis", Type: "string"}}},
		},
		Links: []LinkType{{Name: "runs_on", From: "Workload", To: "Node"}},
	}
}

// reference is the old, whole-store AtRisk.
func reference(r Reader, schema *Schema, failing func(string) bool) []Risk {
	var out []Risk
	for _, o := range r.List("") {
		var bad []string
		for _, k := range BoundKPIs(schema, o) {
			if failing(k) {
				bad = append(bad, k)
			}
		}
		if len(bad) > 0 {
			out = append(out, Risk{ID: o.ID, Type: o.Type, KPIs: bad})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func boundIDs(st *Store) []string {
	var out []string
	for _, o := range st.Bound() {
		out = append(out, o.ID)
	}
	return out
}

func TestBoundIndexTracksTypeOverridesDeletesAndReopens(t *testing.T) {
	both(t, func(t *testing.T, path string) {
		st, _ := Open(path, boundSchema())
		now := time.Now()
		rec := func(typ, key string, props map[string]any) Record {
			return Record{Type: typ, Namespace: "k", Key: key, Props: props}
		}
		_, _ = st.Ingest("x", "t", []Record{
			rec("Node", "n1", map[string]any{"name": "n1"}),
			rec("Workload", "w1", map[string]any{"name": "w1"}),
			rec("Workload", "w2", map[string]any{"name": "w2", "kpis": "latency, errors"}),
			rec("Node", "n2", map[string]any{"name": "n2", "kpis": ""}), // an explicit empty override: bound to nothing
		}, now)
		want := []string{"Node:k:n1", "Workload:k:w2"}
		if got := boundIDs(st); !reflect.DeepEqual(got, want) {
			t.Fatalf("bound = %v, want %v", got, want)
		}
		// A per-object property adds a workload and removes a node.
		_, _ = st.Ingest("x", "t", []Record{rec("Workload", "w1", map[string]any{"kpis": "latency"}), rec("Node", "n1", map[string]any{"kpis": ""})}, now.Add(time.Minute))
		if got := boundIDs(st); !reflect.DeepEqual(got, []string{"Workload:k:w1", "Workload:k:w2"}) {
			t.Fatalf("after overrides: %v", got)
		}
		// Deleting an object removes it from the index.
		st.mu.Lock()
		st.delObject("Workload:k:w2")
		_ = st.save() // the deletion must reach the backend before the reopen below
		st.mu.Unlock()
		if got := boundIDs(st); !reflect.DeepEqual(got, []string{"Workload:k:w1"}) {
			t.Fatalf("after delete: %v", got)
		}
		st.Close()
		re, _ := Open(path, boundSchema())
		defer re.Close()
		if got := boundIDs(re); !reflect.DeepEqual(got, []string{"Workload:k:w1"}) {
			t.Fatalf("not rebuilt on reopen: %v", got)
		}
	})
}

func TestBoundIndexFollowsAnIdentityMerge(t *testing.T) {
	st, _ := Open("", boundSchema())
	_, _ = st.Ingest("a", "t", []Record{{Type: "Node", Namespace: "erp", Key: "1", Props: map[string]any{"name": "Main Node"}}}, time.Now())
	_, _ = st.Ingest("b", "t", []Record{{Type: "Node", Namespace: "mon", Key: "9", Props: map[string]any{"name": "main node"}}}, time.Now())
	if len(boundIDs(st)) != 2 {
		t.Fatalf("both nodes are KPI-bound: %v", boundIDs(st))
	}
	c := st.Candidates()[0]
	if _, err := st.Decide(c.ID, true, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := boundIDs(st); len(got) != 1 || got[0] != c.A {
		t.Fatalf("after the merge only the survivor is bound: %v (survivor %s)", got, c.A)
	}
}

// The indexed summary must equal a scan of every object, whatever mix of
// types, overrides and tenants the store holds.
func TestIndexedAtRiskEqualsFullScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	st, _ := Open("", boundSchema())
	var recs []Record
	for i := 0; i < 600; i++ {
		typ := "Workload"
		if i%7 == 0 {
			typ = "Node"
		}
		props := map[string]any{"name": fmt.Sprint("o", i)}
		switch rng.IntN(5) {
		case 0:
			props["kpis"] = "cpu"
		case 1:
			props["kpis"] = "latency,errors"
		case 2:
			props["kpis"] = ""
		}
		tenant := []string{"", "alpha", "beta"}[rng.IntN(3)]
		recs = append(recs, Record{Type: typ, Namespace: "k", Key: fmt.Sprint(i), Tenant: tenant, Props: props})
	}
	if _, err := st.Ingest("x", "t", recs, time.Now()); err != nil {
		t.Fatal(err)
	}
	failing := func(k string) bool { return k == "cpu" || k == "errors" }
	for _, p := range []Principal{{Roles: []string{"admin"}}, {Roles: []string{"approver"}, Tenant: "alpha"}, {Roles: []string{"viewer"}, Tenant: "beta"}} {
		rd := st.As(nil, p)
		got, want := AtRisk(rd, failing), reference(rd, st.Schema(), failing)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("principal %+v: indexed summary differs from a full scan (%d vs %d risks)", p, len(got), len(want))
		}
		if len(got) == 0 {
			t.Fatal("test data produced no risks")
		}
	}
}

func TestRiskSummaryCostFollowsBoundObjectsNotStoreSize(t *testing.T) {
	st, _ := Open("", boundSchema())
	for off := 0; off < 150000; off += 5000 {
		var recs []Record
		for i := off; i < off+5000; i++ {
			recs = append(recs, Record{Type: "Workload", Namespace: "k", Key: fmt.Sprintf("%07d", i), Props: map[string]any{"name": "w"},
				Links: []RecordLink{{Type: "runs_on", ToType: "Node", ToNS: "k", ToKey: fmt.Sprint(i % 20)}}})
		}
		if off == 0 {
			for n := 0; n < 20; n++ {
				recs = append(recs, Record{Type: "Node", Namespace: "k", Key: fmt.Sprint(n), Props: map[string]any{"name": fmt.Sprint("n", n)}})
			}
		}
		if _, err := st.Ingest("k", "t", recs, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	rd := st.As(nil, Principal{Roles: []string{"admin"}})
	start := time.Now()
	risks := AtRisk(rd, func(k string) bool { return k == "cpu" })
	took := time.Since(start)
	if len(risks) != 20 {
		t.Fatalf("%d risks", len(risks))
	}
	if limit := 50 * time.Millisecond * slowdown; took > limit {
		t.Errorf("a risk summary over 150k objects (20 bound) took %v; it should not scan the store", took)
	}
	t.Logf("AtRisk over %d objects, 20 bound: %v", len(st.List("")), took)
	_ = filepath.Join
}

func TestScanNamesCostOnALargeStore(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	st, _ := Open("", boundSchema())
	for off := 0; off < 150000; off += 5000 {
		var recs []Record
		for i := off; i < off+5000; i++ {
			recs = append(recs, Record{Type: "Workload", Namespace: "k", Key: fmt.Sprintf("%07d", i), Props: map[string]any{"name": fmt.Sprintf("pod-%d", i)}})
		}
		_, _ = st.Ingest("k", "t", recs, time.Now())
	}
	rd := st.As(nil, Principal{Roles: []string{"admin"}})
	start := time.Now()
	n := 0
	rd.Scan("", func(Object) bool { n++; return true })
	took := time.Since(start)
	t.Logf("a full visible scan of %d objects: %v", n, took.Round(time.Millisecond))
	if limit := 2 * time.Second * slowdown; took > limit || n != 150000 {
		t.Errorf("scan of %d objects took %v", n, took)
	}
}
