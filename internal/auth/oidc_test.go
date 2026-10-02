// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIdP is a minimal OpenID provider: discovery, JWKS and a token
// endpoint that enforces PKCE.
type fakeIdP struct {
	ts     *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]authReq
	groups []string
}

type authReq struct{ challenge, nonce string }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newIdP(t *testing.T) *fakeIdP {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: k, codes: map[string]authReq{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.ts.URL, "authorization_endpoint": f.ts.URL + "/authorize", "token_endpoint": f.ts.URL + "/token",
			"jwks_uri": f.ts.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		req, ok := f.codes[r.Form.Get("code")]
		f.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || b64(sum[:]) != req.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 60, "id_token": f.idToken(req.nonce)})
	})
	f.ts = httptest.NewServer(mux)
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeIdP) idToken(nonce string) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	c, _ := json.Marshal(map[string]any{
		"iss": f.ts.URL, "aud": "zyntra", "sub": "u-123", "preferred_username": "dana",
		"nonce": nonce, "groups": f.groups, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
	})
	in := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(in))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return in + "." + b64(sig)
}

func TestOIDCLogin(t *testing.T) {
	idp := newIdP(t)
	a := New("", "")
	err := a.EnableOIDC(context.Background(), OIDCConfig{
		Issuer: idp.ts.URL, ClientID: "zyntra", ClientSecret: "s",
		RoleMap: map[string][]Role{"sre-leads": {RoleApprover}, "ops": {RoleExecutor}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Required() || a.Methods()[0] != "oidc" {
		t.Fatal("oidc should require auth")
	}
	mux := http.NewServeMux()
	a.Routes(mux)
	zs := httptest.NewServer(mux)
	defer zs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	login := func(groups []string, tamperState bool) (*http.Response, string) {
		idp.groups = groups
		resp, err := client.Get(zs.URL + "/api/v1/auth/oidc/login")
		if err != nil || resp.StatusCode != http.StatusFound {
			t.Fatalf("login redirect: %v %v", err, resp)
		}
		var loginCookie *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == oidcCookie {
				loginCookie = c
			}
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		q := loc.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || !strings.HasSuffix(q.Get("redirect_uri"), "/api/v1/auth/oidc/callback") {
			t.Fatalf("authorize url %s", loc)
		}
		idp.mu.Lock()
		idp.codes["c1"] = authReq{challenge: q.Get("code_challenge"), nonce: q.Get("nonce")}
		idp.mu.Unlock()
		state := q.Get("state")
		if tamperState {
			state += "x"
		}
		req, _ := http.NewRequest("GET", zs.URL+"/api/v1/auth/oidc/callback?code=c1&state="+url.QueryEscape(state), nil)
		req.AddCookie(loginCookie)
		cb, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		cb.Body.Close()
		for _, c := range cb.Cookies() {
			if c.Name == CookieName {
				return cb, c.Value
			}
		}
		return cb, ""
	}

	resp, sess := login([]string{"sre-leads", "ops", "other"}, false)
	if resp.StatusCode != http.StatusFound || sess == "" {
		t.Fatalf("callback %d", resp.StatusCode)
	}
	id, ok := a.Valid(sess)
	if !ok || id.Subject != "dana" || id.Method != "oidc" || !id.Has(RoleApprover) || !id.Has(RoleExecutor) || id.Has(RoleAdmin) {
		t.Fatalf("identity %+v", id)
	}
	if resp, _ := login([]string{"sre-leads"}, true); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("state mismatch: %d", resp.StatusCode)
	}
	if resp, sess := login([]string{"strangers"}, false); resp.StatusCode != http.StatusForbidden || sess != "" {
		t.Fatalf("unmapped groups: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", zs.URL+"/api/v1/auth/oidc/callback?code=c1&state=x", nil)
	if resp, _ := client.Do(req); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("callback without login cookie: %d", resp.StatusCode)
	}
}
