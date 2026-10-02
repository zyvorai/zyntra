// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

func restDef(spec string) string {
	return `
objects: [{name: Asset, properties: [{name: name, type: string}, {name: status, type: string}]}]
links: []
connectors:
  - name: erp
    kind: rest
` + spec + `
    fields: {id: id, name: name, status: state}
    mapping: {type: Asset, namespace: erp, key: id, props: {name: name, status: status}}
`
}

func restPull(t *testing.T, spec string, srv *httptest.Server, since time.Time) ([]ontology.Record, error) {
	t.Helper()
	d, err := ontology.ParseDefinition([]byte(restDef(strings.ReplaceAll(spec, "SERVER", srv.URL))))
	if err != nil {
		t.Fatal(err)
	}
	return REST{Spec: d.Connectors[0], Schema: d.Schema()}.Pull(context.Background(), since)
}

func asset(i int) map[string]any {
	return map[string]any{"id": fmt.Sprintf("a%d", i), "name": fmt.Sprintf("Asset %d", i), "state": "ok"}
}

func TestRESTFollowsODataNextLinksWithBearerAuth(t *testing.T) {
	t.Setenv("ERP_TOKEN", "s3cret-token")
	var auths []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		body := map[string]any{"value": []any{asset(page*2 + 1), asset(page*2 + 2)}}
		if page < 2 {
			body["@odata.nextLink"] = fmt.Sprintf("/assets?page=%d", page+1) // relative, like many servers
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	recs, err := restPull(t, "    url: SERVER/assets?page=0\n    token_env: ERP_TOKEN\n    items: value\n    next: '@odata\\.nextLink'", srv, time.Time{})
	if err != nil || len(recs) != 6 {
		t.Fatalf("%d records, %v", len(recs), err)
	}
	if recs[5].Key != "a6" || recs[0].Props["name"] != "Asset 1" {
		t.Errorf("mapping: %+v %+v", recs[0], recs[5])
	}
	for _, a := range auths {
		if a != "Bearer s3cret-token" {
			t.Errorf("a request carried %q", a)
		}
	}
}

func TestRESTPageParamStopsOnAShortPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if r.URL.Query().Get("per_page") != "2" {
			t.Errorf("page size param = %q", r.URL.RawQuery)
		}
		items := []any{asset(page*10 + 1), asset(page*10 + 2)}
		if page == 3 {
			items = items[:1] // the last, short page
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": items})
	}))
	defer srv.Close()
	recs, err := restPull(t, "    url: SERVER/api\n    items: result\n    page_param: page\n    page_size: 2\n    page_size_param: per_page", srv, time.Time{})
	if err != nil || len(recs) != 5 || calls.Load() != 3 {
		t.Fatalf("%d records over %d calls, %v", len(recs), calls.Load(), err)
	}
}

func TestRESTSinceParamAndTopLevelList(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("modified_after")
		_ = json.NewEncoder(w).Encode([]any{asset(1)})
	}))
	defer srv.Close()
	spec := "    url: SERVER/assets\n    since_param: modified_after"
	if _, err := restPull(t, spec, srv, time.Time{}); err != nil || got != "" {
		t.Fatalf("first pull sent since=%q (%v)", got, err)
	}
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if _, err := restPull(t, spec, srv, since); err != nil || got != "2026-10-01T12:00:00Z" {
		t.Fatalf("incremental pull sent since=%q (%v)", got, err)
	}
}

// The token must never reach a host the pack did not configure, whether the
// API points a next link or a redirect there.
func TestRESTNeverSendsCredentialsToAnotherHost(t *testing.T) {
	t.Setenv("ERP_TOKEN", "s3cret-token")
	var leaked atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Add(1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
	}))
	defer evil.Close()
	var link string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{asset(1)}, "next": link})
		}
	}))
	defer srv.Close()

	link = evil.URL + "/steal"
	_, err := restPull(t, "    url: SERVER/a\n    token_env: ERP_TOKEN\n    items: value\n    next: next", srv, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "refusing to send credentials") {
		t.Fatalf("a next link to another host was followed: %v", err)
	}
	link = strings.Replace(srv.URL, "http://", "https://", 1) + "/a" // a scheme downgrade/upgrade is another origin too
	if _, err := restPull(t, "    url: SERVER/a\n    token_env: ERP_TOKEN\n    items: value\n    next: next", srv, time.Time{}); err == nil {
		t.Error("a next link on another scheme was followed")
	}
	_, err = restPull(t, "    url: SERVER/redirect\n    token_env: ERP_TOKEN\n    items: value", srv, time.Time{})
	if err == nil || !strings.Contains(err.Error(), "redirect to another host") {
		t.Fatalf("a cross-host redirect was followed: %v", err)
	}
	if leaked.Load() != 0 {
		t.Fatalf("the token reached another host %d time(s)", leaked.Load())
	}
}

func TestRESTErrorsHideSecrets(t *testing.T) {
	t.Setenv("ERP_TOKEN", "s3cret-token")
	t.Setenv("ERP_PASS", "hunter2")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusForbidden) }))
	defer srv.Close()
	_, err := restPull(t, "    url: SERVER/a?apikey=s3cret-token\n    token_env: ERP_TOKEN\n    items: value", srv, time.Time{})
	if err == nil || strings.Contains(err.Error(), "s3cret-token") {
		t.Fatalf("a secret leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("the status should be reported: %v", err)
	}
	// An unreachable server: the URL in the transport error is redacted too.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	d, _ := ontology.ParseDefinition([]byte(restDef("    url: " + url + "/a?apikey=s3cret-token\n    items: value")))
	_, err = REST{Spec: d.Connectors[0], Schema: d.Schema()}.Pull(context.Background(), time.Time{})
	if err == nil || strings.Contains(err.Error(), "s3cret-token") {
		t.Fatalf("the query string leaked into a transport error: %v", err)
	}
}

func TestRESTLimitsAndOddResponses(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loop":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": []any{asset(1)}, "next": "/loop"})
		case "/html":
			_, _ = w.Write([]byte("<html>login</html>"))
		case "/notlist":
			_ = json.NewEncoder(w).Encode(map[string]any{"value": "x"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"other": []any{}})
		}
	}))
	defer srv.Close()
	cases := map[string]struct{ spec, want string }{
		"endless paging":   {"    url: SERVER/loop\n    items: value\n    next: next", "pages"},
		"not json":         {"    url: SERVER/html\n    items: value", "not JSON"},
		"items not a list": {"    url: SERVER/notlist\n    items: value", "not a list"},
		"missing items":    {"    url: SERVER/x\n    items: value", "no \"value\""},
	}
	for name, c := range cases {
		if _, err := restPull(t, c.spec, srv, time.Time{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRESTSpecValidationAndPrune(t *testing.T) {
	bad := map[string]string{
		"no scheme":        "    url: ftp://x/a",
		"both pagings":     "    url: http://x/a\n    next: n\n    page_param: p",
		"bad param":        "    url: http://x/a\n    page_param: 'p;drop'",
		"half basic auth":  "    url: http://x/a\n    user_env: U",
		"token and basic":  "    url: http://x/a\n    token_env: T\n    user_env: U\n    pass_env: P",
		"huge page":        "    url: http://x/a\n    page_size: 999999",
		"prune on changes": "    url: http://x/a\n    prune: true\n    since_param: since",
	}
	for name, spec := range bad {
		if _, err := ontology.ParseDefinition([]byte(restDef(spec))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := ontology.ParseDefinition([]byte(restDef("    url: https://erp.example/odata/Assets\n    user_env: U\n    pass_env: P\n    prune: true\n    next: '@odata\\.nextLink'\n    items: value"))); err != nil {
		t.Fatalf("a valid connector was refused: %v", err)
	}
}

func TestRESTPruneRemovesDeletedAssets(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []any{asset(1), asset(2)}
		if n.Add(1) > 1 {
			items = items[:1]
		}
		_ = json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()
	d, err := ontology.ParseDefinition([]byte(restDef("    url: " + srv.URL + "\n    prune: true")))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	sc, _ := NewScheduler(st, d, t.TempDir(), nil, Options{}, "")
	if _, err := sc.RunNow(context.Background(), "erp"); err != nil {
		t.Fatal(err)
	}
	rep, err := sc.RunNow(context.Background(), "erp")
	if err != nil || rep.Pruned != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if _, ok := st.Get("Asset:erp:a2"); ok {
		t.Error("an asset the API no longer returns is still an object")
	}
}

// TestRESTAgainstPublicOData reads the public Northwind demo (read-only).
// It runs only when ZYNTRA_TEST_ODATA=1, since it needs the internet.
func TestRESTAgainstPublicOData(t *testing.T) {
	if testing.Short() || os.Getenv("ZYNTRA_TEST_ODATA") != "1" {
		t.Skip("set ZYNTRA_TEST_ODATA=1 to read the public Northwind OData service")
	}
	d, err := ontology.ParseDefinition([]byte(`
objects: [{name: Product, properties: [{name: name, type: string}, {name: price, type: number}, {name: discontinued, type: bool}]}]
links: []
connectors:
  - name: northwind
    kind: rest
    url: https://services.odata.org/V4/Northwind/Northwind.svc/Products
    items: value
    next: '@odata\.nextLink'
    fields: {id: ProductID, name: ProductName, price: UnitPrice, discontinued: Discontinued}
    mapping: {type: Product, namespace: nw, key: id, props: {name: name, price: price, discontinued: discontinued}}
`))
	if err != nil {
		t.Fatal(err)
	}
	recs, err := REST{Spec: d.Connectors[0], Schema: d.Schema()}.Pull(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	rep, err := st.Ingest("rest:northwind", "t", recs, time.Now())
	t.Logf("%d products read, ingest: %+v", len(recs), rep)
	if err != nil || len(recs) < 70 || rep.Objects != len(recs) || len(rep.Skipped) != 0 {
		t.Fatalf("%d records, %+v, %v", len(recs), rep, err)
	}
	p, _ := st.Get("Product:nw:1")
	t.Logf("product 1: %v", p.Props["name"].V)
}
