// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"reflect"
	"testing"
	"time"
)

func TestTenantTypeIndexAndVisibleTypes(t *testing.T) {
	st, _ := Open("", testSchema())
	now := time.Now()
	_, err := st.Ingest("x", "t", []Record{
		{Type: "Cluster", Namespace: "n", Key: "mine", Tenant: "alpha", Props: map[string]any{"name": "a"}},
		{Type: "Customer", Namespace: "n", Key: "theirs", Tenant: "beta", Props: map[string]any{"name": "b"}},
		{Type: "Service", Namespace: "n", Key: "shared", Props: map[string]any{"name": "s"}},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	own, shared := st.TypesIn("alpha")
	if !reflect.DeepEqual(own, []string{"Cluster"}) || !reflect.DeepEqual(shared, []string{"Service"}) {
		t.Fatalf("own %v shared %v", own, shared)
	}
	alpha := Principal{Roles: []string{"approver"}, Tenant: "alpha"}
	// With no access rules a tenant sees its own types only: the shared Service has no rule exposing it.
	if got := st.As(nil, alpha).VisibleTypes(); !reflect.DeepEqual(got, []string{"Cluster"}) {
		t.Fatalf("visible = %v", got)
	}
	// A rule that shares Service with tenants adds it; beta's Customer never appears.
	ac := &Access{Schema: st.Schema(), Rules: []Rule{{SharedTypes: []string{"Service"}}}}
	if got := st.As(ac, alpha).VisibleTypes(); !reflect.DeepEqual(got, []string{"Cluster", "Service"}) {
		t.Fatalf("visible with a shared type = %v", got)
	}
	// A rule that limits the tenant's types removes the others even though objects exist.
	limited := &Access{Schema: st.Schema(), Rules: []Rule{{Roles: []string{"approver"}, Types: []string{"Service"}}, {SharedTypes: []string{"Service"}}}}
	if got := st.As(limited, alpha).VisibleTypes(); !reflect.DeepEqual(got, []string{"Service"}) {
		t.Fatalf("a type hidden by a rule was offered: %v", got)
	}
	// A deployment-wide principal sees every type in the schema.
	if got := st.As(nil, Principal{Roles: []string{"admin"}}).VisibleTypes(); len(got) != 3 {
		t.Fatalf("admin sees %v", got)
	}
	// The index follows deletes and updates: removing the only Cluster takes the type away.
	st.mu.Lock()
	st.delObject("Cluster:n:mine")
	st.mu.Unlock()
	if got := st.As(nil, alpha).VisibleTypes(); len(got) != 0 {
		t.Fatalf("a deleted object's type is still offered: %v", got)
	}
}
