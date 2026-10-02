// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestCookieLifecycle(t *testing.T) {
	a := New("k3y", "")
	now := time.Unix(1_700_000_000, 0)
	a.now = func() time.Time { return now }
	v, _ := a.Mint(time.Hour, newIdentity("jane.doe", "oidc", RoleApprover, RoleViewer))
	valid := func(v string) bool { _, ok := a.Valid(v); return ok }
	if id, ok := a.Valid(v); !ok || id.Subject != "jane.doe" || id.Role != RoleApprover || len(id.Roles) != 2 || id.Method != "oidc" {
		t.Fatalf("fresh cookie: %+v %v", id, ok)
	}
	parts := strings.Split(v, ".")
	exp, sig := parts[0], parts[len(parts)-1]
	if valid(exp + ".oidc.viewer+approver.jane.doe." + strings.Repeat("0", len(sig))) {
		t.Fatal("tampered signature accepted")
	}
	if valid("9999999999.oidc.viewer+approver.jane.doe." + sig) {
		t.Fatal("tampered expiry accepted")
	}
	if valid(exp + ".oidc.viewer+approver.root." + sig) {
		t.Fatal("tampered operator accepted")
	}
	if valid(exp + ".oidc.viewer+admin.jane.doe." + sig) {
		t.Fatal("tampered roles accepted")
	}
	if _, ok := New("other", "").Valid(v); ok {
		t.Fatal("cookie valid under a different key")
	}
	now = now.Add(2 * time.Hour)
	if valid(v) {
		t.Fatal("expired cookie accepted")
	}
}

func TestOperator(t *testing.T) {
	for in, want := range map[string]string{"": "admin", " ops@lab ": "ops@lab", "a b;c": "abc", "<>": "admin"} {
		if got := Operator(in); got != want {
			t.Errorf("Operator(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRequireAndSession(t *testing.T) {
	a := New("k3y", "ex3c")
	mux := http.NewServeMux()
	a.Routes(mux)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(FromContext(r.Context()).Subject)) })
	mux.Handle("GET /admin", a.Require(ok, RoleAdmin))
	mux.Handle("POST /exec", a.Require(ok, RoleExec, RoleAdmin))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	do := func(method, path, bearer string, cookie *http.Cookie, body string) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}

	if c := do("GET", "/admin", "", nil, "").StatusCode; c != 401 {
		t.Fatalf("anonymous: %d", c)
	}
	if c := do("GET", "/admin", "wrong", nil, "").StatusCode; c != 401 {
		t.Fatalf("wrong bearer: %d", c)
	}
	if c := do("GET", "/admin", "k3y", nil, "").StatusCode; c != 200 {
		t.Fatalf("admin bearer: %d", c)
	}
	if c := do("GET", "/admin", "ex3c", nil, "").StatusCode; c != 403 {
		t.Fatalf("exec token on admin route: %d", c)
	}
	if c := do("POST", "/exec", "ex3c", nil, "").StatusCode; c != 200 {
		t.Fatalf("exec token on exec route: %d", c)
	}
	if c := do("POST", "/api/v1/session", "", nil, `{"token":"nope"}`).StatusCode; c != 401 {
		t.Fatalf("bad login: %d", c)
	}
	resp := do("POST", "/api/v1/session", "", nil, `{"token":"k3y","remember":true,"operator":"sre-1"}`)
	var sess *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == CookieName {
			sess = c
		}
	}
	if resp.StatusCode != 200 || sess == nil || !sess.HttpOnly {
		t.Fatalf("login: %d %+v", resp.StatusCode, sess)
	}
	if time.Until(sess.Expires) < 6*24*time.Hour {
		t.Fatalf("remember me should extend expiry, got %v", sess.Expires)
	}
	if c := do("GET", "/admin", "", sess, "").StatusCode; c != 200 {
		t.Fatalf("cookie auth: %d", c)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(sess)
	if id := a.Identify(req); id.Subject != "sre-1" {
		t.Fatalf("session subject %q", id.Subject)
	}
	if c := do("GET", "/api/v1/whoami", "", sess, "").StatusCode; c != 200 {
		t.Fatalf("whoami: %d", c)
	}
}

func TestRolesAndLocalUsers(t *testing.T) {
	a := New("k3y", "")
	hash, _ := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	a.SetUsers([]LocalUser{{Name: "vic", Hash: string(hash), Roles: []Role{RoleViewer}}, {Name: "pat", Hash: string(hash), Roles: []Role{RoleProposer}}})
	mux := http.NewServeMux()
	a.Routes(mux)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	mux.Handle("GET /read", a.Require(ok, Readers...))
	mux.Handle("POST /propose", a.Require(ok, Proposers...))
	mux.Handle("POST /approve", a.Require(ok, Approvers...))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	login := func(user, pw string) *http.Cookie {
		resp, err := http.Post(ts.URL+"/api/v1/session", "application/json", strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == CookieName {
				return c
			}
		}
		return nil
	}
	if login("vic", "wrong") != nil || login("nobody", "s3cret") != nil {
		t.Fatal("bad credentials accepted")
	}
	call := func(method, path string, c *http.Cookie) int {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		req.AddCookie(c)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}
	vic, pat := login("vic", "s3cret"), login("pat", "s3cret")
	for _, c := range []struct {
		cookie       *http.Cookie
		method, path string
		want         int
	}{
		{vic, "GET", "/read", 200}, {vic, "POST", "/propose", 403},
		{pat, "POST", "/propose", 200}, {pat, "POST", "/approve", 403},
	} {
		if got := call(c.method, c.path, c.cookie); got != c.want {
			t.Errorf("%s %s as %s: %d want %d", c.method, c.path, c.cookie.Value[:20], got, c.want)
		}
	}
	if _, err := ParseRole("exec"); err == nil {
		t.Fatal("exec must not be assignable")
	}
	m, err := ParseRoleMap("sre-leads=approver+executor, platform=proposer;everyone=viewer")
	if err != nil || len(m["sre-leads"]) != 2 || m["everyone"][0] != RoleViewer {
		t.Fatalf("role map %v %v", m, err)
	}
	if _, err := ParseRoleMap("x=root"); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestOpenMode(t *testing.T) {
	a := New("", "")
	r := httptest.NewRequest("GET", "/", nil)
	if id := a.Identify(r); id.Role != RoleAdmin || id.Method != "open" {
		t.Fatalf("open mode identity %+v", id)
	}
}
