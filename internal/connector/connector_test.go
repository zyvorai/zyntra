// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

func schema() *ontology.Schema {
	return &ontology.Schema{Objects: []ontology.ObjectType{{Name: "Machine", Properties: []ontology.Property{{Name: "name", Type: "string"}}}}}
}

func TestReadLines(t *testing.T) {
	recs, err := ReadLines(strings.NewReader("{\"type\":\"Machine\",\"namespace\":\"x\",\"key\":\"1\",\"props\":{\"name\":\"a\"}}\n\n{\"type\":\"Machine\",\"namespace\":\"x\",\"key\":\"2\"}\n"))
	if err != nil || len(recs) != 2 {
		t.Fatalf("%v %v", recs, err)
	}
	if _, err := ReadLines(strings.NewReader("not json")); err == nil {
		t.Error("accepted garbage")
	}
}

func TestExecNeedsOptIn(t *testing.T) {
	t.Setenv("ZYNTRA_CONNECTOR_EXEC", "")
	if _, err := (Exec{Spec: "x", Command: []string{"true"}}).Pull(context.Background(), time.Time{}); err == nil {
		t.Fatal("ran without opt-in")
	}
}

func TestExecRunsAndIngests(t *testing.T) {
	t.Setenv("ZYNTRA_CONNECTOR_EXEC", "1")
	st, _ := ontology.Open("", schema())
	c := Exec{Spec: "echo", Command: []string{"sh", "-c", `echo '{"type":"Machine","namespace":"mes","key":"m1","props":{"name":"press"}}'`}}
	rep, err := Run(context.Background(), st, c, time.Time{}, "t", time.Now())
	if err != nil || rep.Objects != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, err := (Exec{Spec: "bad", Command: []string{"sh", "-c", "echo boom >&2; exit 3"}}).Pull(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want stderr in error, got %v", err)
	}
}

func TestHTTPConnector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[{"type":"Machine","namespace":"mes","key":"m9","props":{"name":"lathe"}}]`))
	}))
	defer srv.Close()
	recs, err := HTTP{Spec: "mes", URL: srv.URL, Token: "tok"}.Pull(context.Background(), time.Now())
	if err != nil || len(recs) != 1 {
		t.Fatalf("%v %v", recs, err)
	}
	if _, err := (HTTP{Spec: "mes", URL: srv.URL}).Pull(context.Background(), time.Time{}); err == nil {
		t.Error("accepted 401")
	}
}
