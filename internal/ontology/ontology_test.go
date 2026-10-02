// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"path/filepath"
	"testing"
	"time"
)

func testSchema() *Schema {
	return &Schema{
		Objects: []ObjectType{
			{Name: "Cluster", Properties: []Property{{Name: "name", Type: "string", Required: true}, {Name: "gpus", Type: "number"}}},
			{Name: "Service", Properties: []Property{{Name: "name", Type: "string"}}},
			{Name: "Customer", Properties: []Property{{Name: "name", Type: "string"}, {Name: "contact", Type: "string", Sensitive: true}}},
		},
		Links: []LinkType{
			{Name: "runs_on", From: "Service", To: "Cluster", Cardinality: "many-to-many"},
			{Name: "consumes", From: "Customer", To: "Service"},
		},
	}
}

func prov() Prov {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return Prov{Source: "test", ObservedAt: t, IngestedAt: t}
}

func obj(typ, key string, props map[string]any) Object {
	o := Object{ID: MakeID(typ, "lab", key), Type: typ, Props: map[string]Value{}}
	for k, v := range props {
		o.Props[k] = Value{V: v, Prov: prov()}
	}
	return o
}

func TestSchemaValidate(t *testing.T) {
	cases := map[string]struct {
		mut  func(*Schema)
		want string
	}{
		"ok":              {func(*Schema) {}, ""},
		"duplicate type":  {func(s *Schema) { s.Objects = append(s.Objects, ObjectType{Name: "Cluster"}) }, "duplicate object type"},
		"colon in name":   {func(s *Schema) { s.Objects = append(s.Objects, ObjectType{Name: "A:B"}) }, "may not contain"},
		"bad prop type":   {func(s *Schema) { s.Objects[0].Properties[0].Type = "blob" }, "invalid type"},
		"dangling link":   {func(s *Schema) { s.Links[0].To = "Nope" }, "unknown object type"},
		"bad cardinality": {func(s *Schema) { s.Links[0].Cardinality = "some" }, "unknown cardinality"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := testSchema()
			c.mut(s)
			err := s.Validate()
			if c.want == "" && err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if c.want != "" && (err == nil || !contains(err.Error(), c.want)) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestCheckKPIs(t *testing.T) {
	s := testSchema()
	s.Objects[0].KPIs = []string{"queue_wait", "ghost"}
	err := s.CheckKPIs(func(id string) bool { return id == "queue_wait" })
	if err == nil || !contains(err.Error(), "ghost") {
		t.Fatalf("want ghost flagged, got %v", err)
	}
}

func TestIDRoundTrip(t *testing.T) {
	id := MakeID("Cluster", "lab", "a:b")
	typ, ns, key, ok := SplitID(id)
	if !ok || typ != "Cluster" || ns != "lab" || key != "a:b" {
		t.Fatalf("got %q %q %q %v", typ, ns, key, ok)
	}
	if _, _, _, ok := SplitID("nonsense"); ok {
		t.Fatal("accepted malformed id")
	}
}

func TestUpsertValidation(t *testing.T) {
	st, _ := Open("", testSchema())
	bad := map[string]Object{
		"wrong kind":    obj("Cluster", "c1", map[string]any{"gpus": "many"}),
		"unknown prop":  obj("Cluster", "c1", map[string]any{"colour": "red"}),
		"unknown type":  {ID: "Rack:lab:r1", Type: "Rack"},
		"id/type clash": {ID: "Service:lab:s1", Type: "Cluster"},
	}
	for name, o := range bad {
		if err := st.Upsert(o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	noProv := obj("Cluster", "c1", map[string]any{"name": "x"})
	v := noProv.Props["name"]
	v.Prov.Source = ""
	noProv.Props["name"] = v
	if err := st.Upsert(noProv); err == nil {
		t.Error("accepted a value with no provenance")
	}
}

func TestUpsertMergesProps(t *testing.T) {
	st, _ := Open("", testSchema())
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"name": "gpu-a"}))
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"gpus": 8.0}))
	o, _ := st.Get(MakeID("Cluster", "lab", "c1"))
	if len(o.Props) != 2 {
		t.Fatalf("want both props kept, got %v", o.Props)
	}
	if m := st.Missing(o.ID); len(m) != 0 {
		t.Fatalf("missing %v", m)
	}
	_ = st.Upsert(obj("Cluster", "c2", map[string]any{"gpus": 1.0}))
	if m := st.Missing(MakeID("Cluster", "lab", "c2")); len(m) != 1 || m[0] != "name" {
		t.Fatalf("want name missing, got %v", m)
	}
}

func buildGraph(t *testing.T) *Store {
	t.Helper()
	st, _ := Open("", testSchema())
	for _, o := range []Object{
		obj("Cluster", "c1", map[string]any{"name": "gpu-a"}),
		obj("Service", "s1", nil), obj("Service", "s2", nil),
		obj("Customer", "k1", nil), obj("Customer", "k2", nil),
	} {
		if err := st.Upsert(o); err != nil {
			t.Fatal(err)
		}
	}
	id := func(typ, k string) string { return MakeID(typ, "lab", k) }
	for _, l := range [][3]string{
		{"runs_on", id("Service", "s1"), id("Cluster", "c1")},
		{"runs_on", id("Service", "s2"), id("Cluster", "c1")},
		{"consumes", id("Customer", "k1"), id("Service", "s1")},
		{"consumes", id("Customer", "k2"), id("Service", "s2")},
	} {
		if _, err := st.AddLink(l[0], l[1], l[2], prov()); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestAddLinkChecksEnds(t *testing.T) {
	st := buildGraph(t)
	id := func(typ, k string) string { return MakeID(typ, "lab", k) }
	if _, err := st.AddLink("runs_on", id("Cluster", "c1"), id("Service", "s1"), prov()); err == nil {
		t.Error("accepted reversed link")
	}
	if _, err := st.AddLink("runs_on", id("Service", "s1"), id("Cluster", "ghost"), prov()); err == nil {
		t.Error("accepted missing end")
	}
	before := len(st.Links(id("Cluster", "c1")))
	_, _ = st.AddLink("runs_on", id("Service", "s1"), id("Cluster", "c1"), prov())
	if len(st.Links(id("Cluster", "c1"))) != before {
		t.Error("re-adding a link is not idempotent")
	}
}

func TestImpact(t *testing.T) {
	st := buildGraph(t)
	got := st.Impact(MakeID("Cluster", "lab", "c1"), 0)
	if len(got) != 4 {
		t.Fatalf("want 2 services + 2 customers, got %d", len(got))
	}
	for _, i := range got {
		want := map[string]int{"Service": 1, "Customer": 2}[i.Object.Type]
		if i.Depth != want {
			t.Errorf("%s depth %d, want %d", i.Object.ID, i.Depth, want)
		}
	}
	if d1 := st.Impact(MakeID("Cluster", "lab", "c1"), 1); len(d1) != 2 {
		t.Errorf("depth cap: got %d", len(d1))
	}
	if st.Impact("Cluster:lab:none", 0) != nil {
		t.Error("impact of unknown object")
	}
}

func TestImpactSurvivesCycles(t *testing.T) {
	s := &Schema{
		Objects: []ObjectType{{Name: "N"}},
		Links:   []LinkType{{Name: "dep", From: "N", To: "N"}},
	}
	st, _ := Open("", s)
	a, b := MakeID("N", "x", "a"), MakeID("N", "x", "b")
	_ = st.Upsert(Object{ID: a, Type: "N"})
	_ = st.Upsert(Object{ID: b, Type: "N"})
	_, _ = st.AddLink("dep", a, b, prov())
	_, _ = st.AddLink("dep", b, a, prov())
	if got := st.Impact(a, 0); len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "ontology.json")
	st, _ := Open(path, testSchema())
	if err := st.Upsert(obj("Cluster", "c1", map[string]any{"name": "gpu-a"})); err != nil {
		t.Fatal(err)
	}
	st2, err := Open(path, testSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st2.Get(MakeID("Cluster", "lab", "c1")); !ok {
		t.Fatal("object lost across reopen")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
