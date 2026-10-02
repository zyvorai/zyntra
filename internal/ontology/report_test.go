// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"testing"
	"time"
)

// A customer who appears on two rows is one object; the ingest report (and so
// the Sources card) must say one, not two.
func TestIngestReportCountsDistinctObjectsAndLinks(t *testing.T) {
	st, _ := Open("", testSchema())
	link := RecordLink{Type: "runs_on", ToType: "Cluster", ToNS: "n", ToKey: "c1"}
	rep, err := st.Ingest("x", "t", []Record{
		{Type: "Cluster", Namespace: "n", Key: "c1", Props: map[string]any{"name": "c"}},
		{Type: "Service", Namespace: "n", Key: "s1", Props: map[string]any{"name": "s"}, Links: []RecordLink{link}},
		{Type: "Service", Namespace: "n", Key: "s1", Props: map[string]any{"name": "s"}, Links: []RecordLink{link}}, // the same service again
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Objects != 2 || rep.Links != 1 {
		t.Fatalf("3 records naming 2 objects and 1 link reported %d objects, %d links", rep.Objects, rep.Links)
	}
	if got := len(st.List("")); got != rep.Objects {
		t.Errorf("the report says %d objects but the store holds %d", rep.Objects, got)
	}
}
