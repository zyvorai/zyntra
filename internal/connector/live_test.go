// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

const liveDef = `
objects:
  - name: Node
    match: name
    properties: [{name: name, type: string}, {name: gpus, type: number}, {name: ready, type: string}]
  - name: Pod
    properties: [{name: name, type: string}, {name: phase, type: string}]
links:
  - {name: runs_on, from: Pod, to: Node}
connectors:
  - name: k8s-nodes
    kind: kubernetes
    resource: nodes
    interval: 30s
    fields: {name: metadata.name, gpus: 'status.capacity.nvidia\.com/gpu', ready: 'status.conditions.[type=Ready].status'}
    mapping: {type: Node, namespace: k8s, key: name, props: {name: name, gpus: gpus, ready: ready}}
  - name: k8s-pods
    kind: kubernetes
    resource: pods
    fields: {name: metadata.name, phase: status.phase, node: spec.nodeName}
    mapping:
      type: Pod
      namespace: k8s
      key: name
      props: {name: name, phase: phase}
      links: [{type: runs_on, column: node, to: Node}]
`

const nodesJSON = `{"items":[
 {"metadata":{"name":"gpu-1"},"status":{"capacity":{"nvidia.com/gpu":"8"},"conditions":[{"type":"Ready","status":"True"}]}},
 {"metadata":{"name":"cpu-1"},"status":{"capacity":{"cpu":"16"},"conditions":[{"type":"Ready","status":"False"}]}}]}`
const podsJSON = `{"items":[{"metadata":{"name":"infer-0"},"status":{"phase":"Running"},"spec":{"nodeName":"gpu-1"}}]}`

func kubectl(calls *[]string) KubectlGet {
	return func(_ context.Context, q KubeQuery) (io.ReadCloser, error) {
		*calls = append(*calls, q.Resource+"|"+q.Namespace)
		switch q.Resource {
		case "nodes":
			return io.NopCloser(strings.NewReader(nodesJSON)), nil
		case "pods":
			return io.NopCloser(strings.NewReader(podsJSON)), nil
		}
		return nil, errors.New("not found")
	}
}

func TestPathKeyedSelector(t *testing.T) {
	doc := map[string]any{"status": map[string]any{"conditions": []any{
		map[string]any{"type": "MemoryPressure", "status": "False"},
		map[string]any{"type": "Ready", "status": "True"},
	}}}
	if v, ok := Path(doc, "status.conditions.[type=Ready].status"); !ok || v != "True" {
		t.Errorf("keyed selector = %v %v", v, ok)
	}
	// It does not depend on position.
	rev := map[string]any{"status": map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "False"}, map[string]any{"type": "DiskPressure", "status": "False"},
	}}}
	if v, _ := Path(rev, "status.conditions.[type=Ready].status"); v != "False" {
		t.Errorf("reordered conditions = %v", v)
	}
	for _, bad := range []string{"status.conditions.[type=Nope].status", "status.conditions.[type].status", "status.conditions.[=x].status"} {
		if _, ok := Path(doc, bad); ok {
			t.Errorf("%s resolved", bad)
		}
	}
}

func TestPrunedKubernetesObjectsDisappear(t *testing.T) {
	d, err := ontology.ParseDefinition([]byte(strings.Replace(liveDef, "    resource: pods\n", "    resource: pods\n    prune: true\n", 1)))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	pods := podsJSON
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: func(_ context.Context, q KubeQuery) (io.ReadCloser, error) {
		if q.Resource == "nodes" {
			return io.NopCloser(strings.NewReader(nodesJSON)), nil
		}
		return io.NopCloser(strings.NewReader(pods)), nil
	}}, "")
	if _, err := sc.RunAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("Pod:k8s:infer-0"); !ok {
		t.Fatal("pod missing")
	}
	pods = `{"items":[{"metadata":{"name":"infer-1"},"status":{"phase":"Running"},"spec":{"nodeName":"gpu-1"}}]}`
	if _, err := sc.RunNow(context.Background(), "k8s-pods"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Get("Pod:k8s:infer-0"); ok {
		t.Error("a deleted pod is still in the ontology")
	}
	if _, ok := st.Get("Pod:k8s:infer-1"); !ok {
		t.Error("the new pod is missing")
	}
	if im := st.Impact("Node:k8s:gpu-1", 0); len(im) != 1 || im[0].Object.ID != "Pod:k8s:infer-1" {
		t.Errorf("impact of the node = %+v", im)
	}
	// Nodes were not pruned: that connector has no prune flag.
	if _, ok := st.Get("Node:k8s:cpu-1"); !ok {
		t.Error("an unpruned connector lost an object")
	}
	// prune is refused where it makes no sense.
	if _, err := ontology.ParseDefinition([]byte("objects: [{name: N, properties: [{name: n, type: string}]}]\nlinks: []\nconnectors:\n  - {name: x, kind: exec, command: [y], prune: true}\n")); err == nil {
		t.Error("prune on an exec connector was accepted")
	}
}

func TestPathHandlesEscapesAndArrays(t *testing.T) {
	doc := map[string]any{"status": map[string]any{"capacity": map[string]any{"nvidia.com/gpu": "8"}, "conditions": []any{map[string]any{"type": "Ready"}}}}
	for path, want := range map[string]any{
		"status.capacity.nvidia\\.com/gpu": "8",
		"status.conditions.0.type":         "Ready",
	} {
		if got, ok := Path(doc, path); !ok || got != want {
			t.Errorf("%s = %v %v", path, got, ok)
		}
	}
	for _, path := range []string{"status.nope", "status.conditions.5.type", "status.capacity", "status.conditions"} {
		if _, ok := Path(doc, path); ok {
			t.Errorf("%s should not resolve to a scalar", path)
		}
	}
}

func TestKubernetesConnectorBuildsObjectsAndLinks(t *testing.T) {
	d, err := ontology.ParseDefinition([]byte(liveDef))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	var calls []string
	sc, err := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: kubectl(&calls)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RunAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls, ","); got != "nodes|,pods|" {
		t.Fatalf("kubectl calls = %s", got)
	}
	n, ok := st.Get("Node:k8s:gpu-1")
	if !ok || n.Props["gpus"].V != 8.0 || n.Props["ready"].V != "True" {
		t.Fatalf("node = %+v", n)
	}
	if time.Since(n.Props["gpus"].Prov.ObservedAt) > time.Minute || n.Props["gpus"].Prov.Source != "kubernetes:k8s-nodes" {
		t.Errorf("provenance = %+v", n.Props["gpus"].Prov)
	}
	if im := st.Impact("Node:k8s:gpu-1", 0); len(im) != 1 || im[0].Object.ID != "Pod:k8s:infer-0" {
		t.Fatalf("pod should depend on its node: %+v", im)
	}
	if _, ok := st.Get("Node:k8s:cpu-1"); !ok {
		t.Error("a node without a gpu capacity should still be an object")
	}
}

func TestSpecValidation(t *testing.T) {
	base := `
objects: [{name: Node, properties: [{name: name, type: string}]}]
links: []
connectors:
`
	cases := map[string]string{
		"flag in resource": "  - {name: a, kind: kubernetes, resource: '--all', fields: {n: x}, mapping: {type: Node, namespace: k, key: n}}\n",
		"no fields":        "  - {name: a, kind: kubernetes, resource: nodes, mapping: {type: Node, namespace: k, key: n}}\n",
		"unknown type":     "  - {name: a, kind: kubernetes, resource: nodes, fields: {n: x}, mapping: {type: Nope, namespace: k, key: n}}\n",
		"write query":      "  - {name: a, kind: sql, driver: sqlite, dsn_env: X, query: 'DELETE FROM t', mapping: {type: Node, namespace: k, key: n}}\n",
		"two statements":   "  - {name: a, kind: sql, driver: sqlite, dsn_env: X, query: 'select 1; drop table t', mapping: {type: Node, namespace: k, key: n}}\n",
		"inline dsn":       "  - {name: a, kind: sql, driver: mysql, dsn_env: X, query: 'select 1', mapping: {type: Node, namespace: k, key: n}}\n",
		"short interval":   "  - {name: a, kind: kubernetes, interval: 1s, resource: nodes, fields: {n: x}, mapping: {type: Node, namespace: k, key: n}}\n",
		"reserved name":    "  - {name: pack-files, kind: exec, command: [x]}\n",
		"duplicate":        "  - {name: a, kind: exec, command: [x]}\n  - {name: a, kind: exec, command: [x]}\n",
	}
	for name, c := range cases {
		if _, err := ontology.ParseDefinition([]byte(base + c)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	ok := base + "  - {name: a, kind: sql, driver: postgres, dsn_env: PG, query: 'SELECT id, name FROM assets WHERE updated_at > $1', interval: 1m, mapping: {type: Node, namespace: erp, key: id, props: {name: name}}}\n"
	if _, err := ontology.ParseDefinition([]byte(ok)); err != nil {
		t.Fatalf("a valid sql connector was refused: %v", err)
	}
}

func sqliteDB(t *testing.T) (path string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "erp.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"CREATE TABLE assets (id TEXT, name TEXT, gpus INTEGER, updated_at TEXT)",
		"INSERT INTO assets VALUES ('a1','Press 4',0,'2026-10-01T00:00:00Z')",
		"INSERT INTO assets VALUES ('a2','Lathe 2',0,'2026-10-01T12:00:00Z')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestSQLConnectorIncrementalPull(t *testing.T) {
	path := sqliteDB(t)
	t.Setenv("ERP_DSN", "file:"+path)
	d, err := ontology.ParseDefinition([]byte(`
objects: [{name: Machine, properties: [{name: name, type: string}, {name: gpus, type: number}]}]
links: []
connectors:
  - name: erp
    kind: sql
    driver: sqlite
    dsn_env: ERP_DSN
    query: "SELECT id, name, gpus FROM assets WHERE updated_at > ?"
    mapping: {type: Machine, namespace: erp, key: id, props: {name: name, gpus: gpus}}
`))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	sc, err := NewScheduler(st, d, t.TempDir(), nil, Options{}, "")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := sc.RunNow(context.Background(), "erp")
	if err != nil || rep.Objects != 2 {
		t.Fatalf("first pull: %+v %v", rep, err)
	}
	if o, _ := st.Get("Machine:erp:a2"); o.Props["name"].V != "Lathe 2" || o.Props["gpus"].V != 0.0 {
		t.Fatalf("row mapping: %+v", o)
	}
	// Nothing changed since the first pull started: the second pull is empty.
	rep, err = sc.RunNow(context.Background(), "erp")
	if err != nil || rep.Objects != 0 {
		t.Fatalf("incremental pull returned %d rows (%v)", rep.Objects, err)
	}
}

func TestSQLConnectorRefusesWritesAndHidesDSN(t *testing.T) {
	t.Setenv("ERP_DSN", "file:/nonexistent-dir/secret-password-123/x.db")
	spec := ontology.ConnectorSpec{Name: "x", Kind: "sql", Driver: "sqlite", DSNEnv: "ERP_DSN", Query: "DELETE FROM assets",
		Mapping: &ontology.Mapping{Type: "Machine", Namespace: "e", Key: "id"}}
	q := &SQL{Spec: spec, Schema: &ontology.Schema{Objects: []ontology.ObjectType{{Name: "Machine"}}}}
	if _, err := q.Pull(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "SELECT") {
		t.Fatalf("a write query ran: %v", err)
	}
	spec.Query = "SELECT 1"
	q = &SQL{Spec: spec, Schema: q.Schema}
	_, err := q.Pull(context.Background(), time.Time{})
	if err == nil || strings.Contains(err.Error(), "secret-password-123") {
		t.Fatalf("the connection string leaked into an error: %v", err)
	}
	spec.DSNEnv = "UNSET_VAR"
	if _, err := (&SQL{Spec: spec, Schema: q.Schema}).Pull(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "UNSET_VAR") {
		t.Errorf("a missing dsn variable should be named: %v", err)
	}
}

func TestSchedulerBackoffHealthAndRestore(t *testing.T) {
	d, _ := ontology.ParseDefinition([]byte(liveDef))
	st, _ := ontology.Open("", d.Schema())
	var fail atomic.Bool
	run := func(ctx context.Context, q KubeQuery) (io.ReadCloser, error) {
		if fail.Load() {
			return nil, errors.New("connection refused to https://user:tok@api.example")
		}
		var c []string
		return kubectl(&c)(ctx, q)
	}
	state := filepath.Join(t.TempDir(), "connectors.json")
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: run}, state)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	sc.now = func() time.Time { return now }
	status := func(name string) Status {
		for _, s := range sc.Statuses() {
			if s.Name == name {
				return s
			}
		}
		t.Fatalf("no job %s", name)
		return Status{}
	}
	if _, err := sc.RunNow(context.Background(), "k8s-nodes"); err != nil {
		t.Fatal(err)
	}
	s := status("k8s-nodes")
	if !s.Healthy || s.Runs != 1 || s.LastObjects != 2 || s.NextRun != now.Add(30*time.Second) {
		t.Fatalf("after success: %+v", s)
	}
	cursor := s.LastStart
	fail.Store(true)
	var waits []time.Duration
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		_, _ = sc.RunNow(context.Background(), "k8s-nodes")
		s = status("k8s-nodes")
		waits = append(waits, s.NextRun.Sub(now))
	}
	want := []time.Duration{60 * time.Second, 120 * time.Second, 240 * time.Second, 240 * time.Second, 240 * time.Second}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("backoff = %v, want %v", waits, want)
		}
	}
	if s.Healthy || s.Failures != 5 || s.Streak != 5 || s.LastError == "" {
		t.Fatalf("after failures: %+v", s)
	}
	if !s.LastStart.Equal(cursor) {
		t.Error("a failed run moved the incremental cursor")
	}
	fail.Store(false)
	now = now.Add(time.Minute)
	if _, err := sc.RunNow(context.Background(), "k8s-nodes"); err != nil {
		t.Fatal(err)
	}
	if s = status("k8s-nodes"); !s.Healthy || s.Streak != 0 || s.LastError != "" {
		t.Fatalf("recovery: %+v", s)
	}
	// Health lapses when runs stop for three intervals.
	now = now.Add(4 * time.Minute)
	if status("k8s-nodes").Healthy {
		t.Error("a stale connector still reports healthy")
	}
	// State survives a restart.
	again, _ := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: run}, state)
	for _, r := range again.Statuses() {
		if r.Name == "k8s-nodes" && (r.Runs != 7 || r.LastStart.IsZero()) {
			t.Errorf("restored status = %+v", r)
		}
	}
	if _, err := sc.RunNow(context.Background(), "nope"); err == nil {
		t.Error("unknown job accepted")
	}
}

func TestSchedulerLongErrorsAreClipped(t *testing.T) {
	d, _ := ontology.ParseDefinition([]byte(liveDef))
	st, _ := ontology.Open("", d.Schema())
	long := strings.Repeat("x ", 500)
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: func(context.Context, KubeQuery) (io.ReadCloser, error) { return nil, errors.New(long) }}, "")
	_, _ = sc.RunNow(context.Background(), "k8s-nodes")
	for _, s := range sc.Statuses() {
		if s.Name == "k8s-nodes" && len(s.LastError) > maxErrorLen+4 {
			t.Errorf("error not clipped: %d", len(s.LastError))
		}
	}
}

func TestRunAndSingleFlight(t *testing.T) {
	d, _ := ontology.ParseDefinition([]byte(liveDef))
	st, _ := ontology.Open("", d.Schema())
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: func(context.Context, KubeQuery) (io.ReadCloser, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return io.NopCloser(strings.NewReader(nodesJSON)), nil
	}}, "")
	done := make(chan error, 1)
	go func() { _, err := sc.RunNow(context.Background(), "k8s-nodes"); done <- err }()
	<-started
	if _, err := sc.RunNow(context.Background(), "k8s-nodes"); err == nil || err.Error() != "already running" {
		t.Fatalf("a concurrent run was allowed: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFileFactsAreDatedByModificationTime(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "a.csv")
	if err := os.WriteFile(csv, []byte("id,name\n1,Old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(csv, old, old); err != nil {
		t.Fatal(err)
	}
	d, _ := ontology.ParseDefinition([]byte(`
objects: [{name: Node, properties: [{name: name, type: string}]}]
links: []
mappings: [{source: a.csv, type: Node, namespace: f, key: id, props: {name: name}}]
`))
	st, _ := ontology.Open("", d.Schema())
	load := func(path, _ string) (any, error) {
		return []any{map[string]any{"id": "1", "name": "Old"}}, nil
	}
	sc, _ := NewScheduler(st, d, dir, load, Options{}, "")
	if _, err := sc.RunAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	o, _ := st.Get("Node:f:1")
	got := o.Props["name"].Prov
	if got.ObservedAt.Sub(old).Abs() > time.Second {
		t.Fatalf("observed %s, want the file's mtime %s", got.ObservedAt, old)
	}
	if time.Since(got.IngestedAt) > time.Minute {
		t.Error("ingest time should be now")
	}
}

// TestPostgresConnector runs against a real server when
// ZYNTRA_TEST_POSTGRES_DSN is set (e.g. "host=/tmp/sock port=55432 dbname=postgres sslmode=disable").
func TestPostgresConnector(t *testing.T) {
	dsn := os.Getenv("ZYNTRA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ZYNTRA_TEST_POSTGRES_DSN to run against a Postgres server")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		"DROP TABLE IF EXISTS zyntra_assets",
		"CREATE TABLE zyntra_assets (id text, name text, gpus integer, updated_at timestamptz)",
		"INSERT INTO zyntra_assets VALUES ('p1','Press 4',0, now() - interval '2 days'), ('p2','Lathe 2',8, now() - interval '1 hour')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS zyntra_assets") })
	t.Setenv("PG_DSN", dsn)
	d, err := ontology.ParseDefinition([]byte(`
objects: [{name: Machine, properties: [{name: name, type: string}, {name: gpus, type: number}]}]
links: []
connectors:
  - name: erp
    kind: sql
    driver: postgres
    dsn_env: PG_DSN
    query: "SELECT id, name, gpus, updated_at FROM zyntra_assets WHERE updated_at > $1::timestamptz"
    mapping: {type: Machine, namespace: erp, key: id, props: {name: name, gpus: gpus}, observed: updated_at}
`))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{}, "")
	rep, err := sc.RunNow(context.Background(), "erp")
	if err != nil || rep.Objects != 2 {
		t.Fatalf("first pull: %+v %v", rep, err)
	}
	o, _ := st.Get("Machine:erp:p2")
	if o.Props["gpus"].V != 8.0 || time.Since(o.Props["name"].Prov.ObservedAt) > 2*time.Hour {
		t.Fatalf("row/observed mapping: %+v", o.Props)
	}
	// An old row keeps its own timestamp as the observation time.
	if p1, _ := st.Get("Machine:erp:p1"); time.Since(p1.Props["name"].Prov.ObservedAt) < 47*time.Hour {
		t.Errorf("the observed column was ignored: %v", p1.Props["name"].Prov.ObservedAt)
	}
	if _, err := db.Exec("UPDATE zyntra_assets SET gpus = 16, updated_at = now() WHERE id = 'p1'"); err != nil {
		t.Fatal(err)
	}
	rep, err = sc.RunNow(context.Background(), "erp")
	if err != nil || rep.Objects != 1 {
		t.Fatalf("incremental pull: %+v %v", rep, err)
	}
	// The read-only transaction really refuses writes even if a query tried one.
	q := &SQL{Spec: ontology.ConnectorSpec{Name: "w", Driver: "postgres", DSNEnv: "PG_DSN", Query: "WITH x AS (DELETE FROM zyntra_assets RETURNING id) SELECT id FROM x",
		Mapping: &ontology.Mapping{Type: "Machine", Namespace: "e", Key: "id"}}, Schema: d.Schema()}
	if _, err := q.Pull(context.Background(), time.Time{}); err == nil {
		t.Error("a data-modifying CTE ran inside the connector")
	}
	var n int
	_ = db.QueryRow("SELECT count(*) FROM zyntra_assets").Scan(&n)
	if n != 2 {
		t.Fatalf("rows left = %d; the connector deleted data", n)
	}
}

func TestItemsToRowsStreamsAndSurvivesOddShapes(t *testing.T) {
	fields := map[string]string{"name": "metadata.name"}
	// Other top-level keys, before and after items, are skipped.
	rows, err := ItemsToRows(strings.NewReader(`{"apiVersion":"v1","kind":"List","metadata":{"resourceVersion":"1"},"items":[{"metadata":{"name":"a"}},{"metadata":{"name":"b"}}],"extra":[1,2,3]}`), fields)
	if err != nil || len(rows) != 2 || rows[1].(map[string]any)["name"] != "b" {
		t.Fatalf("%v %v", rows, err)
	}
	if rows, err := ItemsToRows(strings.NewReader(`{"items":[]}`), fields); err != nil || len(rows) != 0 {
		t.Errorf("empty list: %v %v", rows, err)
	}
	for name, bad := range map[string]string{"not an object": `[1]`, "items not a list": `{"items":3}`, "truncated": `{"items":[{"metadata":{"na`} {
		if _, err := ItemsToRows(strings.NewReader(bad), fields); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// A pull the size of a real cluster (12k pods) must not hold the whole
// listing in memory as decoded JSON.
func TestLargeListingMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; i < 12000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"metadata":{"name":"pod-%d","namespace":"default","uid":"u%d","labels":{"a":"b","c":"d"},"annotations":{"x":"%s"}},"spec":{"nodeName":"n1","containers":[{"name":"c","image":"i","env":[%s]}]},"status":{"phase":"Running"}}`,
			i, i, strings.Repeat("y", 400), strings.Repeat(`{"name":"E","value":"v"},`, 20)+`{"name":"Z","value":"v"}`)
	}
	b.WriteString(`]}`)
	raw := b.String()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rows, err := ItemsToRows(strings.NewReader(raw), map[string]string{"uid": "metadata.uid", "name": "metadata.name", "node": "spec.nodeName"})
	runtime.ReadMemStats(&after)
	if err != nil || len(rows) != 12000 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
	t.Logf("listing %d MB; allocations while streaming: %d MB total", len(raw)>>20, (after.TotalAlloc-before.TotalAlloc)>>20)
	runtime.KeepAlive(rows)
}

func TestNextRunCountsFromTheEndAndSlowIsFlagged(t *testing.T) {
	d, _ := ontology.ParseDefinition([]byte(liveDef))
	st, _ := ontology.Open("", d.Schema())
	var c []string
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: kubectl(&c)}, "")
	// A 30s connector whose run takes 25s: the clock reads 12:00:00 at the
	// start and 12:00:25 at the end.
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	var calls int
	sc.now = func() time.Time {
		calls++
		if calls <= 2 { // start of execute, and the ingest timestamp inside the run
			return base
		}
		return base.Add(25 * time.Second)
	}
	if _, err := sc.RunNow(context.Background(), "k8s-nodes"); err != nil {
		t.Fatal(err)
	}
	for _, s := range sc.Statuses() {
		if s.Name != "k8s-nodes" {
			continue
		}
		if want := base.Add(25*time.Second + 30*time.Second); !s.NextRun.Equal(want) {
			t.Errorf("next run %v, want %v (interval counted from the end of the run)", s.NextRun, want)
		}
		if !s.Slow {
			t.Errorf("a 25s run on a 30s interval should be flagged slow: %+v", s)
		}
	}
}
