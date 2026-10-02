// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	oidcCookie = "zyntra_oidc"
	oidcTTL    = 10 * time.Minute
)

// OIDCConfig configures sign-in through an OpenID Connect provider.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is the callback URL registered with the provider; when
	// empty it is derived from the request host.
	RedirectURL string
	Scopes      []string
	// GroupsClaim names the ID-token claim listing the user's groups.
	GroupsClaim string
	// RoleMap maps groups to Zyntra roles.
	RoleMap map[string][]Role
	// DefaultRole is given to users whose groups map to nothing; empty
	// refuses them.
	DefaultRole Role
}

type oidcState struct {
	cfg      OIDCConfig
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    oauth2.Config
}

// ParseRoleMap reads "group=role[+role],group2=role" (also ";"-separated).
func ParseRoleMap(s string) (map[string][]Role, error) {
	out := map[string][]Role{}
	for _, item := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		g, rs, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || strings.TrimSpace(g) == "" {
			return nil, fmt.Errorf("role map entry %q must be group=role", item)
		}
		for r := range strings.SplitSeq(rs, "+") {
			role, err := ParseRole(r)
			if err != nil {
				return nil, err
			}
			out[strings.TrimSpace(g)] = append(out[strings.TrimSpace(g)], role)
		}
	}
	return out, nil
}

// EnableOIDC discovers the provider and turns on the OIDC routes. It must be
// called before Routes.
func (a *Auth) EnableOIDC(ctx context.Context, cfg OIDCConfig) error {
	if cfg.Issuer == "" || cfg.ClientID == "" {
		return fmt.Errorf("oidc needs an issuer and client id")
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{oidc.ScopeOpenID, "profile", "email", "groups"}
	}
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery for %s: %w", cfg.Issuer, err)
	}
	a.oidc = &oidcState{
		cfg: cfg, provider: p,
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			Endpoint: p.Endpoint(), Scopes: cfg.Scopes, RedirectURL: cfg.RedirectURL,
		},
	}
	return nil
}

func (o *oidcState) config(r *http.Request) oauth2.Config {
	c := o.oauth
	if c.RedirectURL == "" {
		scheme := "http"
		if secure(r) {
			scheme = "https"
		}
		c.RedirectURL = scheme + "://" + r.Host + "/api/v1/auth/oidc/callback"
	}
	return c
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// The login cookie holds exp.state.nonce.verifier.sig; every field is
// base64url so none contains a dot.
func (a *Auth) oidcLogin(w http.ResponseWriter, r *http.Request) {
	state, nonce, verifier := random(), random(), oauth2.GenerateVerifier()
	exp := strconv.FormatInt(a.now().Add(oidcTTL).Unix(), 10)
	v := strings.Join([]string{exp, state, nonce, verifier, a.mac("zyntra-oidc", exp, state, nonce, verifier)}, ".")
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: v, Path: "/api/v1/auth/oidc", MaxAge: int(oidcTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r),
	})
	c := a.oidc.config(r)
	http.Redirect(w, r, c.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

func (a *Auth) oidcFail(w http.ResponseWriter, code int, msg string) {
	http.Error(w, "Sign-in failed: "+msg, code)
}

func (a *Auth) oidcCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(oidcCookie)
	if err != nil {
		a.oidcFail(w, http.StatusBadRequest, "sign-in session missing or expired; start again")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/oidc", MaxAge: -1, HttpOnly: true, Secure: secure(r)})
	parts := strings.Split(c.Value, ".")
	if len(parts) != 5 || !secureEq([]byte(parts[4]), []byte(a.mac("zyntra-oidc", parts[0], parts[1], parts[2], parts[3]))) {
		a.oidcFail(w, http.StatusBadRequest, "invalid sign-in session")
		return
	}
	exp, _ := strconv.ParseInt(parts[0], 10, 64)
	state, nonce, verifier := parts[1], parts[2], parts[3]
	if a.now().Unix() >= exp {
		a.oidcFail(w, http.StatusBadRequest, "sign-in session expired; start again")
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		a.oidcFail(w, http.StatusUnauthorized, "provider returned "+e)
		return
	}
	if !secureEq([]byte(r.URL.Query().Get("state")), []byte(state)) {
		a.oidcFail(w, http.StatusBadRequest, "state mismatch")
		return
	}
	cfg := a.oidc.config(r)
	tok, err := cfg.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		a.oidcFail(w, http.StatusUnauthorized, "code exchange failed")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := a.oidc.verifier.Verify(r.Context(), raw)
	if err != nil {
		a.oidcFail(w, http.StatusUnauthorized, "invalid ID token")
		return
	}
	if !secureEq([]byte(idt.Nonce), []byte(nonce)) {
		a.oidcFail(w, http.StatusUnauthorized, "nonce mismatch")
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		a.oidcFail(w, http.StatusUnauthorized, "unreadable claims")
		return
	}
	roles := a.oidc.roles(claims)
	if len(roles) == 0 {
		a.oidcFail(w, http.StatusForbidden, "your groups do not map to a Zyntra role")
		return
	}
	subject := idt.Subject
	for _, k := range []string{"preferred_username", "email"} {
		if s, ok := claims[k].(string); ok && s != "" {
			subject = s
			break
		}
	}
	a.setSession(w, r, newIdentity(Operator(subject), "oidc", roles...), SessionTTL)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (o *oidcState) roles(claims map[string]any) []Role {
	var groups []string
	switch g := claims[o.cfg.GroupsClaim].(type) {
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				groups = append(groups, s)
			}
		}
	case string:
		groups = strings.Fields(strings.ReplaceAll(g, ",", " "))
	}
	var roles []Role
	for _, g := range groups {
		roles = append(roles, o.cfg.RoleMap[g]...)
	}
	if len(roles) == 0 && o.cfg.DefaultRole != RoleNone {
		roles = []Role{o.cfg.DefaultRole}
	}
	return normalize(roles)
}
