// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package executor

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyvorai/zyntra/internal/graph"
)

type apiCall struct {
	method, path, query, contentType, body string
}

// fakeAPI is an API server holding Gryvia objects by path.
func fakeAPI(t *testing.T, objects map[string]string) (Runner, *[]apiCall) {
	t.Helper()
	var calls []apiCall
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, apiCall{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"), string(b)})
		if r.Header.Get("Authorization") != "Bearer sa-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.Path, "/forbidden") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"gryviapriorities is forbidden"}`))
			return
		}
		obj, ok := objects[r.URL.Path]
		switch {
		case r.Method == http.MethodPatch && r.Header.Get("Content-Type") == "application/apply-patch+yaml":
			_, _ = w.Write(b)
		case !ok:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		default:
			_, _ = w.Write([]byte(obj))
		}
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("sa-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	return kubeAPIAt(dir, host, port, srv.Client()), &calls
}

func renderFor(t *testing.T, template string, params map[string]string) Rendered {
	t.Helper()
	r, err := renderKubectl(graph.Action{ID: "act", Execute: &graph.Execute{Template: template, Params: params}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestKubeAPIApply(t *testing.T) {
	run, calls := fakeAPI(t, nil)
	r := renderFor(t, "gravia.priority", map[string]string{"name": "inference-high", "value": "1000"})
	for _, mode := range []Mode{ModeDryRun, ModeApply} {
		res := (&Executor{Mode: mode, Run: run}).Execute(context.Background(), r)
		if !res.OK {
			t.Fatalf("%s: %+v", mode, res)
		}
		if !strings.Contains(res.Output, "gryviapriority inference-high applied") {
			t.Fatalf("output %q", res.Output)
		}
	}
	c := (*calls)[0]
	if c.method != http.MethodPatch || c.path != "/apis/gryvia.io/v1alpha1/gryviapriorities/inference-high" ||
		c.contentType != "application/apply-patch+yaml" {
		t.Fatalf("call %+v", c)
	}
	if !strings.Contains(c.query, "dryRun=All") || !strings.Contains(c.query, "fieldManager=zyntra") {
		t.Fatalf("dry-run query %q", c.query)
	}
	if strings.Contains((*calls)[1].query, "dryRun") {
		t.Fatalf("apply query %q", (*calls)[1].query)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(c.body), &obj); err != nil || obj["kind"] != "GryviaPriority" {
		t.Fatalf("body %s", c.body)
	}
}

func TestKubeAPIPatchAndDelete(t *testing.T) {
	job := "/apis/gryvia.io/v1alpha1/namespaces/tenant-a/gryviaaijobs/train"
	mine := "/apis/gryvia.io/v1alpha1/gryviapriorities/mine"
	theirs := "/apis/gryvia.io/v1alpha1/gryviapriorities/theirs"
	run, calls := fakeAPI(t, map[string]string{
		job:    `{"kind":"GryviaAIJob","metadata":{"name":"train"}}`,
		mine:   `{"kind":"GryviaPriority","metadata":{"name":"mine","labels":{"app.kubernetes.io/managed-by":"zyntra"}}}`,
		theirs: `{"kind":"GryviaPriority","metadata":{"name":"theirs"}}`,
	})
	ex := &Executor{Mode: ModeApply, Run: run}
	ctx := context.Background()

	res := ex.Execute(ctx, renderFor(t, "gravia.job-suspend", map[string]string{"job": "train", "namespace": "tenant-a"}))
	if !res.OK || (*calls)[0].contentType != "application/merge-patch+json" || (*calls)[0].body != `{"spec":{"suspend":true}}` {
		t.Fatalf("patch %+v %+v", res, (*calls)[0])
	}

	*calls = nil
	res = ex.Execute(ctx, renderFor(t, "gravia.priority-delete", map[string]string{"name": "mine"}))
	if !res.OK || len(*calls) != 2 || (*calls)[1].method != http.MethodDelete {
		t.Fatalf("delete mine %+v %+v", res, *calls)
	}
	*calls = nil
	res = ex.Execute(ctx, renderFor(t, "gravia.priority-delete", map[string]string{"name": "theirs"}))
	if !res.OK || len(*calls) != 1 || !strings.Contains(res.Output, "not managed by zyntra") {
		t.Fatalf("delete theirs %+v %+v", res, *calls)
	}
	res = ex.Execute(ctx, renderFor(t, "gravia.priority-delete", map[string]string{"name": "gone"}))
	if !res.OK || !strings.Contains(res.Output, "nothing to delete") {
		t.Fatalf("delete gone %+v", res)
	}
}

func TestKubeAPIRefusals(t *testing.T) {
	run, calls := fakeAPI(t, nil)
	ctx := context.Background()
	cases := map[string][]string{
		"does not run kubectl get": {"get", "secrets"},
		"cannot write":             {"patch", "secrets", "x", "--type", "merge", "-p", "{}"},
		"managed-by=zyntra":        {"delete", "gryviapriorities.gryvia.io", "--field-selector", "metadata.name=x"},
	}
	for want, args := range cases {
		if _, err := run(ctx, args, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := run(ctx, []string{"apply", "-f", "-"}, "apiVersion: v1\nkind: Secret\nmetadata: {name: x}\n"); err == nil {
		t.Error("applied a Secret")
	}
	if len(*calls) != 0 {
		t.Fatalf("refused commands reached the API: %+v", *calls)
	}
	_, err := run(ctx, []string{"apply", "-f", "-"}, "apiVersion: gryvia.io/v1alpha1\nkind: GryviaPriority\nmetadata: {name: forbidden}\n")
	if err == nil || !strings.Contains(err.Error(), "kubernetes.actions=true") {
		t.Fatalf("forbidden: %v", err)
	}
}
