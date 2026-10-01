// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestEnsureServesVerifiableLoopbackCert(t *testing.T) {
	dir := t.TempDir()
	f, cert, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(f.Key); st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v", st.Mode().Perm())
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	pem, _ := os.ReadFile(f.CA)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatal("CA not parseable")
	}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("client trusting only the CA failed: %v", err)
	}
	resp.Body.Close()

	again, _, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	pem2, _ := os.ReadFile(again.CA)
	if string(pem2) != string(pem) {
		t.Fatal("Ensure regenerated a still-valid CA")
	}
}
