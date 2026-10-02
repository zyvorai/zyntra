// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestMemoryPerObject reports the retained heap per object for a realistic
// shape (4 properties, one link). It only prints unless ZYNTRA_MEM_N is set.
func TestMemoryPerObject(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("ZYNTRA_MEM_N"))
	if n == 0 {
		t.Skip("set ZYNTRA_MEM_N=100000 to measure")
	}
	st, _ := Open("", podSchema())
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	now := time.Now()
	for off := 0; off < n; off += 5000 {
		recs := []Record{}
		if off == 0 {
			recs = append(recs, Record{Type: "Node", Namespace: "k", Key: "n1", Props: map[string]any{"name": "n1"}})
		}
		for i := off; i < off+5000 && i < n; i++ {
			recs = append(recs, Record{Type: "Workload", Namespace: "k", Key: fmt.Sprintf("uid-%d", i), SourceID: fmt.Sprintf("kubernetes:k8s-pods#%d", i),
				Props: map[string]any{"name": fmt.Sprintf("pod-%d", i), "namespace": "default", "phase": "Running"},
				Links: []RecordLink{{Type: "runs_on", ToType: "Node", ToNS: "k", ToKey: "n1"}}})
		}
		if _, err := st.IngestSnapshot("kubernetes:k8s-pods", "t", recs, now); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	per := float64(after.HeapAlloc-before.HeapAlloc) / float64(n)
	t.Logf("%d objects: %.0f MB retained, %.0f bytes per object (with its link)", n, float64(after.HeapAlloc-before.HeapAlloc)/1e6, per)
	runtime.KeepAlive(st)
}
