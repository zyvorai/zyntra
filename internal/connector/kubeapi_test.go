// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zyvorai/zyntra/internal/ontology"
)

// fakeAPI is a TLS server standing in for the Kubernetes API, plus the service
// account directory a pod would have.
type fakeAPI struct {
	srv   *httptest.Server
	dir   string
	host  string
	port  string
	mu    sync.Mutex
	seen  []string // "path?query|Authorization"
	reply func(r *http.Request) (int, string)
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{dir: t.TempDir()}
	f.reply = func(r *http.Request) (int, string) {
		return 200, `{"items":[{"metadata":{"name":"a"}},{"metadata":{"name":"b"}}]}`
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.seen = append(f.seen, r.URL.RequestURI()+"|"+r.Header.Get("Authorization"))
		f.mu.Unlock()
		code, body := f.reply(r)
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	f.host, f.port, _ = net.SplitHostPort(f.srv.Listener.Addr().String())
	cert := f.srv.Certificate()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(filepath.Join(f.dir, "ca.crt"), ca, 0o600); err != nil {
		t.Fatal(err)
	}
	f.setToken(t, "tok-1")
	return f
}

func (f *fakeAPI) setToken(t *testing.T, v string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "token"), []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeAPI) get(t *testing.T, q KubeQuery) (string, error) {
	t.Helper()
	rc, err := kubeAPIAt(f.dir, f.host, f.port, nil)(context.Background(), q)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b), nil
}

func (f *fakeAPI) last() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[len(f.seen)-1]
}

func TestInClusterClientBuildsTheRightRequests(t *testing.T) {
	f := newFakeAPI(t)
	cases := []struct {
		q    KubeQuery
		want string
	}{
		{KubeQuery{Resource: "pods"}, "/api/v1/pods|Bearer tok-1"},
		{KubeQuery{Resource: "pods", Namespace: "kube-system"}, "/api/v1/namespaces/kube-system/pods|Bearer tok-1"},
		{KubeQuery{Resource: "nodes", Namespace: "ignored"}, "/api/v1/nodes|Bearer tok-1"}, // cluster-scoped: no namespace in the path
		{KubeQuery{Resource: "deployments", Namespace: "inference"}, "/apis/apps/v1/namespaces/inference/deployments|Bearer tok-1"},
		{KubeQuery{Resource: "ingresses"}, "/apis/networking.k8s.io/v1/ingresses|Bearer tok-1"},
		{KubeQuery{Resource: "pods", Selector: "app=a,tier!=b", FieldSelector: "status.phase=Running"}, "/api/v1/pods?fieldSelector=status.phase%3DRunning&labelSelector=app%3Da%2Ctier%21%3Db|Bearer tok-1"},
	}
	for _, c := range cases {
		body, err := f.get(t, c.q)
		if err != nil || !strings.Contains(body, `"items"`) {
			t.Fatalf("%+v: %v %q", c.q, err, body)
		}
		if got := f.last(); got != c.want {
			t.Errorf("%+v\n got  %s\n want %s", c.q, got, c.want)
		}
	}
}

func TestInClusterClientRereadsTheRotatingToken(t *testing.T) {
	f := newFakeAPI(t)
	if _, err := f.get(t, KubeQuery{Resource: "nodes"}); err != nil {
		t.Fatal(err)
	}
	f.setToken(t, "tok-2")
	if _, err := f.get(t, KubeQuery{Resource: "nodes"}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(); !strings.HasSuffix(got, "|Bearer tok-2") {
		t.Errorf("a rotated token was not picked up: %s", got)
	}
}

func TestInClusterClientRefusesWhatItShouldNot(t *testing.T) {
	f := newFakeAPI(t)
	for _, res := range []string{"secrets", "Secrets", "nope", "pods/log", "../secrets"} {
		_, err := f.get(t, KubeQuery{Resource: res})
		if err == nil || !strings.Contains(err.Error(), "cannot read") {
			t.Errorf("%q was allowed: %v", res, err)
		}
	}
	f.mu.Lock()
	n := len(f.seen)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("a refused resource still reached the API %d time(s)", n)
	}
	// And the pack validator refuses secrets for any mode, kubectl included.
	_, err := ontology.ParseDefinition([]byte("objects: [{name: N, properties: [{name: n, type: string}]}]\nlinks: []\nconnectors:\n  - {name: x, kind: kubernetes, resource: secrets, fields: {n: metadata.name}, mapping: {type: N, namespace: k, key: n}}\n"))
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Errorf("a pack asking for secrets was accepted: %v", err)
	}
	for _, name := range KubeResourceNames() {
		if name == "secrets" {
			t.Error("secrets is in the readable table")
		}
	}
}

func TestInClusterClientErrorsAreActionable(t *testing.T) {
	f := newFakeAPI(t)
	f.reply = func(*http.Request) (int, string) {
		return 403, `{"kind":"Status","message":"pods is forbidden: User \"system:serviceaccount:z:zyntra\" cannot list resource \"pods\""}`
	}
	_, err := f.get(t, KubeQuery{Resource: "pods"})
	if err == nil || !strings.Contains(err.Error(), "cannot list resource") || !strings.Contains(err.Error(), "kubernetes.inCluster=true") {
		t.Fatalf("a 403 should say what to grant: %v", err)
	}
	// A server error with no body still reports its status.
	f.reply = func(*http.Request) (int, string) { return 503, "" }
	if _, err := f.get(t, KubeQuery{Resource: "pods"}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("%v", err)
	}
	// Missing token or CA, and a CA that is not a certificate.
	if _, err := kubeAPIAt(t.TempDir(), f.host, f.port, nil)(context.Background(), KubeQuery{Resource: "pods"}); err == nil || !strings.Contains(err.Error(), "token") {
		t.Errorf("missing token: %v", err)
	}
	os.WriteFile(filepath.Join(f.dir, "ca.crt"), []byte("not a cert"), 0o600)
	if _, err := f.get(t, KubeQuery{Resource: "pods"}); err == nil || !strings.Contains(err.Error(), "CA") {
		t.Errorf("bad CA: %v", err)
	}
}

func TestInClusterDetection(t *testing.T) {
	dir := t.TempDir()
	if inClusterAt(dir, "10.0.0.1", "443") {
		t.Error("no token file, yet reported in-cluster")
	}
	os.WriteFile(filepath.Join(dir, "token"), []byte("t"), 0o600)
	if !inClusterAt(dir, "10.0.0.1", "443") {
		t.Error("a pod with a token was not detected")
	}
	if inClusterAt(dir, "", "443") || inClusterAt(dir, "10.0.0.1", "") {
		t.Error("detected without the service host/port")
	}
}

// End to end: a Kubernetes connector reads pods through the in-cluster client
// and builds objects and links, with no kubectl involved.
func TestKubernetesConnectorOverTheInClusterClient(t *testing.T) {
	f := newFakeAPI(t)
	f.reply = func(r *http.Request) (int, string) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/nodes") {
			return 200, nodesJSON
		}
		return 200, podsJSON
	}
	d, err := ontology.ParseDefinition([]byte(liveDef))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := ontology.Open("", d.Schema())
	sc, err := NewScheduler(st, d, t.TempDir(), nil, Options{Kubectl: kubeAPIAt(f.dir, f.host, f.port, nil)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sc.RunAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, ok := st.Get("Node:k8s:gpu-1"); !ok || n.Props["gpus"].V != 8.0 {
		t.Fatalf("node = %+v", n)
	}
	if im := st.Impact("Node:k8s:gpu-1", 0); len(im) != 1 || im[0].Object.ID != "Pod:k8s:infer-0" {
		t.Fatalf("the pod should depend on its node: %+v", im)
	}
}

// TestRealCluster runs the in-cluster client against a real API server. Set
// ZYNTRA_TEST_KUBE_DIR to a directory holding "token" and "ca.crt" and
// ZYNTRA_TEST_KUBE_ADDR to host:port. The service account is expected to be
// allowed to get and list nodes only.
func TestRealCluster(t *testing.T) {
	dir, addr := os.Getenv("ZYNTRA_TEST_KUBE_DIR"), os.Getenv("ZYNTRA_TEST_KUBE_ADDR")
	if dir == "" || addr == "" {
		t.Skip("set ZYNTRA_TEST_KUBE_DIR and ZYNTRA_TEST_KUBE_ADDR to run against a real cluster")
	}
	host, port, _ := net.SplitHostPort(addr)
	get := kubeAPIAt(dir, host, port, nil)
	rc, err := get(context.Background(), KubeQuery{Resource: "nodes"})
	if err != nil {
		t.Fatalf("nodes: %v", err)
	}
	rows, err := ItemsToRows(rc, map[string]string{"name": "metadata.name", "ready": "status.conditions.[type=Ready].status"})
	rc.Close()
	if err != nil || len(rows) == 0 {
		t.Fatalf("nodes: %d rows, %v", len(rows), err)
	}
	t.Logf("nodes: %v", rows)
	// Not granted: the error must say what to grant.
	if _, err = get(context.Background(), KubeQuery{Resource: "pods"}); err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), "kubernetes.inCluster=true") {
		t.Fatalf("pods should be forbidden with guidance, got: %v", err)
	}
	t.Logf("pods without a grant: %v", err)
	if _, err = get(context.Background(), KubeQuery{Resource: "secrets"}); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("secrets must be refused client-side: %v", err)
	}
}
