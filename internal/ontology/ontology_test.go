// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSchema() *Schema {
	return &Schema{
		Objects: []ObjectType{
			{Name: "Cluster", Properties: []Property{{Name: "name", Type: "string", Required: true}, {Name: "gpus", Type: "number"}, {Name: "kpis", Type: "string"}}},
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
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestCheckKPIs(t *testing.T) {
	s := testSchema()
	s.Objects[0].KPIs = []string{"queue_wait", "ghost"}
	err := s.CheckKPIs(func(id string) bool { return id == "queue_wait" })
	if err == nil || !strings.Contains(err.Error(), "ghost") {
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

func TestAccessFiltering(t *testing.T) {
	st := buildGraph(t)
	_ = st.Upsert(Object{ID: MakeID("Customer", "lab", "k1"), Type: "Customer", Tenant: "acme",
		Props: map[string]Value{"contact": {V: "a@b.c", Prov: prov()}}})
	_ = st.Upsert(Object{ID: MakeID("Customer", "lab", "k3"), Type: "Customer", Tenant: "other",
		Props: map[string]Value{"name": {V: "O", Prov: prov()}}})
	ac := &Access{Schema: st.Schema(), Rules: []Rule{
		{Roles: []string{"viewer"}, Tenant: "acme", Types: []string{"Customer", "Service"}},
		{Roles: []string{"viewer"}, Deny: []string{"name"}},
	}}
	viewer := st.As(ac, Principal{Subject: "v", Roles: []string{"viewer"}})
	admin := st.As(ac, Principal{Subject: "a", Roles: []string{"admin"}})
	if _, ok := viewer.Get(MakeID("Cluster", "lab", "c1")); ok {
		t.Error("viewer saw a type outside its rule")
	}
	if _, ok := viewer.Get(MakeID("Customer", "lab", "k3")); ok {
		t.Error("viewer saw another tenant")
	}
	k1, _ := viewer.Get(MakeID("Customer", "lab", "k1"))
	if _, ok := k1.Props["contact"]; ok {
		t.Error("sensitive property leaked to viewer")
	}
	if a1, _ := admin.Get(MakeID("Customer", "lab", "k1")); a1.Props["contact"].V != "a@b.c" {
		t.Error("admin lost sensitive property")
	}
	// The viewer must not learn about the cluster through impact either.
	if got := viewer.Impact(MakeID("Cluster", "lab", "c1"), 0); got != nil {
		t.Errorf("impact of a hidden root returned %d objects", len(got))
	}
	for _, i := range viewer.Impact(MakeID("Service", "lab", "s1"), 0) {
		if i.Object.Type == "Cluster" {
			t.Error("hidden type in impact")
		}
	}
	if viewer.Candidates() != nil {
		t.Error("viewer can see candidates")
	}
	if !ac.CanAct(Principal{Roles: []string{"viewer"}}, "x") {
		t.Error("no action limit set, should allow")
	}
}

func TestResolutionQueueAndMerge(t *testing.T) {
	s := testSchema()
	s.Objects[0].Match = "name"
	st, _ := Open("", s)
	var notes []string
	st.Audit = func(sub, by, note string) { notes = append(notes, sub+" "+note) }
	now := time.Now()
	rep, err := st.Ingest("erp", "t", []Record{
		{Type: "Cluster", Namespace: "erp", Key: "A-1", Props: map[string]any{"name": "GPU Cluster A"}, Aliases: []Alias{{"erp", "A-1"}}},
		{Type: "Service", Namespace: "erp", Key: "s", Props: map[string]any{"name": "svc"},
			Links: []RecordLink{{Type: "runs_on", ToType: "Cluster", ToNS: "erp", ToKey: "A-1"}}},
	}, now)
	if err != nil || rep.Objects != 2 || rep.Links != 1 || len(notes) != 1 {
		t.Fatalf("%+v %v %v", rep, err, notes)
	}
	// Same data again is not a change and is not audited.
	rep, _ = st.Ingest("erp", "t", []Record{{Type: "Cluster", Namespace: "erp", Key: "A-1", Props: map[string]any{"name": "GPU Cluster A"}, Aliases: []Alias{{"erp", "A-1"}}}}, now.Add(time.Hour))
	if rep.Changed || len(notes) != 1 {
		t.Fatalf("unchanged ingest was audited: %+v %v", rep, notes)
	}
	// A monitoring system reports the same machine under another id.
	rep, _ = st.Ingest("netra", "t", []Record{{Type: "Cluster", Namespace: "netra", Key: "dev-77", Props: map[string]any{"name": "gpu-cluster-a"}}}, now)
	if rep.Candidates != 1 || len(st.List("Cluster")) != 2 {
		t.Fatalf("want a queued candidate and no auto-merge: %+v", rep)
	}
	c := st.Candidates()[0]
	if _, err := st.Decide(c.ID, true, "alice"); err != nil {
		t.Fatal(err)
	}
	if len(st.List("Cluster")) != 1 {
		t.Fatal("merge did not remove the duplicate")
	}
	if o, ok := st.Get(c.B); !ok || o.ID != c.A {
		t.Fatal("old id does not redirect to the merged object")
	}
	if got := st.Impact(c.A, 0); len(got) != 1 {
		t.Fatalf("links did not follow the merge: %d", len(got))
	}
	if _, err := st.Decide(c.ID, false, "bob"); err == nil {
		t.Error("decided twice")
	}
	// An alias seen again lands on the surviving object.
	rep, _ = st.Ingest("erp", "t", []Record{{Type: "Cluster", Namespace: "erp", Key: "A-1b", Props: map[string]any{"gpus": 8.0}, Aliases: []Alias{{"erp", "A-1"}}}}, now)
	if len(st.List("Cluster")) != 1 {
		t.Fatalf("alias did not pin identity: %+v", rep)
	}
}

func TestIngestSkipsBadRowsKeepsRest(t *testing.T) {
	st, _ := Open("", testSchema())
	rep, _ := st.Ingest("x", "t", []Record{
		{Type: "Cluster", Namespace: "n", Key: "ok", Props: map[string]any{"name": "a"}},
		{Type: "Cluster", Namespace: "n", Key: "bad", Props: map[string]any{"gpus": "lots"}},
		{Type: "Rack", Namespace: "n", Key: "r"},
	}, time.Now())
	if rep.Objects != 1 || len(rep.Skipped) != 2 {
		t.Fatalf("%+v", rep)
	}
}

func TestAtRiskAndExposed(t *testing.T) {
	st := buildGraph(t)
	st.schema.Objects[0].KPIs = []string{"queue_wait"}
	st.RebuildBindings()
	rd := st.As(nil, Principal{})
	risks := AtRisk(rd, func(k string) bool { return k == "queue_wait" })
	if len(risks) != 1 || risks[0].Type != "Cluster" {
		t.Fatalf("%+v", risks)
	}
	ex := Exposed(rd, risks)
	if len(ex) != 4 || ex[0].KPIs[0] != "queue_wait" {
		t.Fatalf("%+v", ex)
	}
	// A per-object override beats the type binding.
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"kpis": "other"}))
	if r := AtRisk(rd, func(k string) bool { return k == "queue_wait" }); len(r) != 0 {
		t.Fatalf("override ignored: %+v", r)
	}
}

func TestHistoryRecordsOnlyRealChanges(t *testing.T) {
	st, _ := Open("", testSchema())
	id := MakeID("Cluster", "lab", "c1")
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"name": "a", "gpus": 8.0}))
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"name": "a", "gpus": 8.0})) // same values
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"gpus": 16.0}))
	h := st.History(id)
	if len(h) != 3 {
		t.Fatalf("want 2 creations and 1 change, got %d: %+v", len(h), h)
	}
	last := h[2]
	if last.Property != "gpus" || last.Before == nil || last.Before.V != 8.0 || last.After.V != 16.0 {
		t.Fatalf("change = %+v", last)
	}
}

func TestHistoryUsesCurrentAccess(t *testing.T) {
	st, _ := Open("", testSchema())
	_ = st.Upsert(obj("Customer", "k1", map[string]any{"name": "Acme", "contact": "a@b.c"}))
	ac := &Access{Schema: st.Schema(), Rules: []Rule{{Roles: []string{"viewer"}, Deny: []string{"name"}}}}
	id := MakeID("Customer", "lab", "k1")
	viewer := st.As(ac, Principal{Roles: []string{"viewer"}})
	admin := st.As(ac, Principal{Roles: []string{"admin"}})
	if got := viewer.History(id); len(got) != 0 {
		t.Fatalf("viewer saw denied or sensitive history: %+v", got)
	}
	if got := admin.History(id); len(got) != 2 {
		// the deny rule names viewers only; the admin sees name and contact
		t.Fatalf("admin history = %+v", got)
	}
}

func TestDigestTracksValuesNotTimes(t *testing.T) {
	st, _ := Open("", testSchema())
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"name": "a"}))
	refs := []ObjectRef{{Input: "cluster", ID: MakeID("Cluster", "lab", "c1"), Type: "Cluster"}}
	d1 := st.Digest(refs)
	o := obj("Cluster", "c1", map[string]any{"name": "a"})
	v := o.Props["name"]
	v.Prov.ObservedAt = v.Prov.ObservedAt.Add(time.Hour)
	o.Props["name"] = v
	_ = st.Upsert(o)
	if st.Digest(refs) != d1 {
		t.Error("a later observation of the same value changed the digest")
	}
	_ = st.Upsert(obj("Cluster", "c1", map[string]any{"name": "b"}))
	if st.Digest(refs) == d1 {
		t.Error("a different value did not change the digest")
	}
}

func TestIngestScopedIsTenantBoundAndAtomic(t *testing.T) {
	st, _ := Open("", testSchema())
	now := time.Now()
	other := Record{Type: "Cluster", Namespace: "x", Key: "theirs", Tenant: "beta", Props: map[string]any{"name": "Beta cluster"}}
	if _, err := st.IngestScoped("beta", "crm", "t", []Record{other}, now); err != nil {
		t.Fatal(err)
	}
	// alpha cannot overwrite beta's object, even inside an otherwise valid batch.
	batch := []Record{
		{Type: "Cluster", Namespace: "x", Key: "mine", Props: map[string]any{"name": "Alpha cluster"}},
		{Type: "Cluster", Namespace: "x", Key: "theirs", Props: map[string]any{"name": "hijacked"}},
	}
	if _, err := st.IngestScoped("alpha", "crm", "t", batch, now); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("want a cross-tenant refusal, got %v", err)
	}
	if _, ok := st.Get(MakeID("Cluster", "x", "mine")); ok {
		t.Error("a rejected batch left a record behind")
	}
	if o, _ := st.Get(MakeID("Cluster", "x", "theirs")); o.Props["name"].V != "Beta cluster" {
		t.Error("the other tenant's object was changed")
	}
	// An alias must not pin onto another tenant's object either.
	_ = st.Upsert(Object{ID: MakeID("Cluster", "x", "aliased"), Type: "Cluster", Tenant: "beta",
		Props: map[string]Value{"name": {V: "n", Prov: prov()}}, Aliases: []Alias{{"erp", "A1"}}})
	via := Record{Type: "Cluster", Namespace: "y", Key: "z", Aliases: []Alias{{"erp", "A1"}}, Props: map[string]any{"name": "x"}}
	if _, err := st.IngestScoped("alpha", "crm", "t", []Record{via}, now); err != nil {
		t.Fatalf("an alias held by another tenant must simply not match: %v", err)
	}
	if o, _ := st.Get(MakeID("Cluster", "x", "aliased")); o.Props["name"].V != "n" {
		t.Error("an alias pinned a record onto another tenant's object")
	}
	if o, ok := st.Get(MakeID("Cluster", "y", "z")); !ok || o.Tenant != "alpha" {
		t.Errorf("the record should be a new object in its own tenant: %+v", o)
	}
	if _, err := st.IngestScoped("alpha", "crm", "t", []Record{{Type: "Cluster", Namespace: "x", Key: "k", Tenant: "beta"}}, now); err == nil {
		t.Error("a record naming another tenant was accepted")
	}
	if rep, err := st.IngestScoped("alpha", "crm", "t", []Record{{Type: "Cluster", Namespace: "x", Key: "ok", Props: map[string]any{"name": "fine"}}}, now); err != nil || rep.Objects != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if o, _ := st.Get(MakeID("Cluster", "x", "ok")); o.Tenant != "alpha" {
		t.Errorf("record landed in tenant %q", o.Tenant)
	}
}

func TestCanIngest(t *testing.T) {
	ac := &Access{Rules: []Rule{
		{Roles: []string{"ingest"}, IngestTenants: []string{"alpha"}},
		{Roles: []string{"super"}, IngestTenants: []string{"*"}},
	}}
	cases := []struct {
		roles  []string
		tenant string
		want   bool
	}{
		{[]string{"ingest"}, "alpha", true},
		{[]string{"ingest"}, "beta", false},
		{[]string{"super"}, "beta", true},
		{[]string{"admin"}, "beta", true},
		{[]string{"approver"}, "alpha", false},
	}
	for _, c := range cases {
		if got := ac.CanIngest(Principal{Roles: c.roles}, c.tenant); got != c.want {
			t.Errorf("%v into %q = %v, want %v", c.roles, c.tenant, got, c.want)
		}
	}
	if (*Access)(nil).CanIngest(Principal{Roles: []string{"ingest"}}, "alpha") {
		t.Error("no access rules must mean no ingest for non-admins")
	}
}

func TestTenantPrincipalSeesOnlyItsTenant(t *testing.T) {
	st, _ := Open("", testSchema())
	put := func(key, tenant string) {
		_ = st.Upsert(Object{ID: MakeID("Cluster", "x", key), Type: "Cluster", Tenant: tenant,
			Props: map[string]Value{"name": {V: key, Prov: prov()}}})
	}
	put("a", "alpha")
	put("b", "beta")
	put("shared", "")
	alpha := Principal{Roles: []string{"approver"}, Tenant: "alpha"}
	for _, ac := range []*Access{nil, {Schema: st.Schema()}} {
		got := st.As(ac, alpha).List("")
		if len(got) != 1 || got[0].Tenant != "alpha" {
			t.Fatalf("tenant principal saw %+v (access %v)", got, ac != nil)
		}
	}
	ac := &Access{Schema: st.Schema(), Rules: []Rule{{SharedTypes: []string{"Cluster"}}}}
	if got := st.As(ac, alpha).List(""); len(got) != 2 {
		t.Fatalf("shared types should add the tenant-less cluster, got %d", len(got))
	}
	for _, o := range st.As(ac, alpha).List("") {
		if o.Tenant == "beta" {
			t.Error("another tenant's object is visible")
		}
	}
	// Merge candidates never cross tenants.
	s2 := testSchema()
	s2.Objects[0].Match = "name"
	st2, _ := Open("", s2)
	_, _ = st2.Ingest("x", "t", []Record{
		{Type: "Cluster", Namespace: "a", Key: "1", Tenant: "alpha", Props: map[string]any{"name": "Main Cluster"}},
		{Type: "Cluster", Namespace: "b", Key: "1", Tenant: "beta", Props: map[string]any{"name": "main cluster"}},
	}, time.Now())
	if n := len(st2.Candidates()); n != 0 {
		t.Fatalf("a cross-tenant merge candidate was proposed (%d)", n)
	}
}

func TestSnapshotIngestPrunesOnlyWhatItAloneVouchesFor(t *testing.T) {
	st, _ := Open("", testSchema())
	var notes []string
	st.Audit = func(sub, by, note string) { notes = append(notes, note) }
	now := time.Now()
	rec := func(key string) Record {
		return Record{Type: "Cluster", Namespace: "k", Key: key, Props: map[string]any{"name": key}}
	}
	svc := Record{Type: "Service", Namespace: "k", Key: "s", Props: map[string]any{"name": "svc"},
		Links: []RecordLink{{Type: "runs_on", ToType: "Cluster", ToNS: "k", ToKey: "gone"}}}
	if _, err := st.IngestSnapshot("k8s", "t", []Record{rec("keep"), rec("gone"), rec("shared")}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Ingest("other", "t", []Record{svc, {Type: "Cluster", Namespace: "k", Key: "shared", Props: map[string]any{"gpus": 4.0}}}, now); err != nil {
		t.Fatal(err)
	}
	if len(st.Links(MakeID("Cluster", "k", "gone"))) != 1 {
		t.Fatal("setup: the service should link to the cluster")
	}
	// The next full listing no longer mentions "gone" or "shared".
	rep, err := st.IngestSnapshot("k8s", "t", []Record{rec("keep")}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pruned != 1 {
		t.Fatalf("pruned %d, want 1 (only 'gone': 'shared' also has a fact from another source)", rep.Pruned)
	}
	if _, ok := st.Get(MakeID("Cluster", "k", "gone")); ok {
		t.Error("a vanished object lingered")
	}
	if len(st.Links(MakeID("Service", "k", "s"))) != 0 {
		t.Error("a link to a pruned object was left dangling")
	}
	for _, id := range []string{"keep", "shared"} {
		if _, ok := st.Get(MakeID("Cluster", "k", id)); !ok {
			t.Errorf("%s was pruned", id)
		}
	}
	if _, ok := st.Get(MakeID("Service", "k", "s")); !ok {
		t.Error("another source's object was pruned")
	}
	if last := notes[len(notes)-1]; !strings.Contains(last, "pruned 1") {
		t.Errorf("the audit note should say what was pruned: %q", last)
	}
	// An empty listing, or one where every record failed, never wipes anything.
	before := len(st.List(""))
	if rep, _ := st.IngestSnapshot("k8s", "t", nil, now.Add(2*time.Minute)); rep.Pruned != 0 || len(st.List("")) != before {
		t.Errorf("an empty snapshot pruned %d", rep.Pruned)
	}
	bad := []Record{{Type: "Cluster", Namespace: "k", Key: "x", Props: map[string]any{"nope": 1}}}
	if rep, _ := st.IngestSnapshot("k8s", "t", bad, now.Add(3*time.Minute)); rep.Pruned != 0 || len(st.List("")) != before {
		t.Errorf("an all-invalid snapshot pruned %d", rep.Pruned)
	}
	// A plain Ingest never prunes.
	if rep, _ := st.Ingest("k8s", "t", []Record{rec("keep")}, now); rep.Pruned != 0 {
		t.Error("Ingest pruned")
	}
}

func TestPruneSurvivesAReopenOnSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")
	st, _ := Open(path, testSchema())
	r := func(k string) Record {
		return Record{Type: "Cluster", Namespace: "k", Key: k, Props: map[string]any{"name": k}}
	}
	_, _ = st.IngestSnapshot("k8s", "t", []Record{r("a"), r("b")}, time.Now())
	_, _ = st.IngestSnapshot("k8s", "t", []Record{r("a")}, time.Now())
	st.Close()
	re, _ := Open(path, testSchema())
	defer re.Close()
	if len(re.List("Cluster")) != 1 {
		t.Fatalf("a pruned object came back after a reopen: %d", len(re.List("Cluster")))
	}
}
