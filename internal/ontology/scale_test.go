// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func podSchema() *Schema {
	return &Schema{
		Objects: []ObjectType{
			{Name: "Node", Properties: []Property{{Name: "name", Type: "string"}}},
			{Name: "Workload", Properties: []Property{{Name: "name", Type: "string"}, {Name: "namespace", Type: "string"}, {Name: "phase", Type: "string"}}},
		},
		Links: []LinkType{{Name: "runs_on", From: "Workload", To: "Node"}},
	}
}

func podRecords(n int) []Record {
	recs := []Record{{Type: "Node", Namespace: "k", Key: "n1", Props: map[string]any{"name": "n1"}}}
	for i := 0; i < n; i++ {
		recs = append(recs, Record{Type: "Workload", Namespace: "k", Key: fmt.Sprintf("uid-%d", i),
			Props: map[string]any{"name": fmt.Sprintf("pod-%d", i), "namespace": "default", "phase": "Running"},
			Links: []RecordLink{{Type: "runs_on", ToType: "Node", ToNS: "k", ToKey: "n1"}}})
	}
	return recs
}

// A cluster-sized pull (12k pods, each linked to one node) must ingest in
// seconds, first time and when nothing changed.
func TestClusterSizedIngestIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	for _, ext := range []string{"json", "db"} {
		t.Run(ext, func(t *testing.T) {
			st, err := Open(filepath.Join(t.TempDir(), "o."+ext), podSchema())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			n := 12000 / (slowdown/5 + 1) // 12k pods normally; a fifth of that under the race detector
			recs := podRecords(n)
			t0 := time.Now()
			rep, err := st.Ingest("k8s", "t", recs, time.Now())
			first := time.Since(t0)
			if err != nil || rep.Objects != n+1 || rep.Links != n {
				t.Fatalf("%+v %v", rep, err)
			}
			t0 = time.Now()
			rep, _ = st.Ingest("k8s", "t", recs, time.Now().Add(time.Minute))
			again := time.Since(t0)
			t.Logf("%s: first ingest %v, unchanged re-ingest %v (changed=%v)", ext, first.Round(time.Millisecond), again.Round(time.Millisecond), rep.Changed)
			limit := 8 * time.Second * slowdown
			if first > limit || again > limit {
				t.Errorf("too slow: first %v, again %v", first, again)
			}
			t0 = time.Now()
			im := st.Impact("Node:k:n1", 0)
			t.Logf("impact of the node: %d objects in %v", len(im), time.Since(t0).Round(time.Millisecond))
		})
	}
}
