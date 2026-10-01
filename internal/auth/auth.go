// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package auth gates the API with a shared admin key, exchanged by the UI for
// an HMAC-signed HttpOnly session cookie. With no key configured the API is
// open (development mode).
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	CookieName  = "zyntra_session"
	SessionTTL  = 12 * time.Hour
	RememberTTL = 7 * 24 * time.Hour
)

type Role string

const (
	RoleNone  Role = ""
	RoleAdmin Role = "admin"
	// RoleExec may only call the execution endpoint (Keep's broker uses it).
	RoleExec Role = "exec"
)

type Identity struct {
	Subject string `json:"subject"`
	Role    Role   `json:"role"`
	Method  string `json:"method"`
}

type ctxKey struct{}

func FromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(ctxKey{}).(Identity)
	return id
}

type Auth struct {
	key     []byte
	execKey []byte
	now     func() time.Time
}

// New returns an Auth. An empty apiKey disables authentication.
func New(apiKey, execKey string) *Auth {
	return &Auth{key: []byte(apiKey), execKey: []byte(execKey), now: time.Now}
}

func (a *Auth) Required() bool { return len(a.key) > 0 }

func secureEq(a, b []byte) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare(a, b) == 1
}

func (a *Auth) sign(exp int64, operator string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte("zyntra-session." + strconv.FormatInt(exp, 10) + "." + operator))
	return hex.EncodeToString(m.Sum(nil))
}

// Operator normalizes a display name recorded in the audit trail.
func Operator(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 64 {
		s = s[:64]
	}
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '@':
			return r
		}
		return -1
	}, s)
	if out == "" {
		return "admin"
	}
	return out
}

// Mint returns a cookie value for operator valid for ttl.
func (a *Auth) Mint(ttl time.Duration, operator string) (string, time.Time) {
	operator = Operator(operator)
	exp := a.now().Add(ttl)
	e := exp.Unix()
	return strconv.FormatInt(e, 10) + "." + operator + "." + a.sign(e, operator), exp
}

// Valid returns the operator of an unexpired, correctly signed cookie.
func (a *Auth) Valid(v string) (string, bool) {
	if !a.Required() {
		return "", false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return "", false
	}
	expStr, sig := parts[0], parts[len(parts)-1]
	operator := strings.Join(parts[1:len(parts)-1], ".")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || a.now().Unix() >= exp || Operator(operator) != operator {
		return "", false
	}
	if !hmac.Equal([]byte(sig), []byte(a.sign(exp, operator))) {
		return "", false
	}
	return operator, true
}

// Identify resolves the caller from a Bearer token or session cookie.
func (a *Auth) Identify(r *http.Request) Identity {
	if !a.Required() {
		return Identity{Subject: "anonymous", Role: RoleAdmin, Method: "open"}
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		tok = strings.TrimSpace(tok)
		switch {
		case secureEq(a.key, []byte(tok)):
			return Identity{Subject: "admin", Role: RoleAdmin, Method: "bearer"}
		case secureEq(a.execKey, []byte(tok)):
			return Identity{Subject: "keep-broker", Role: RoleExec, Method: "exec-token"}
		}
		return Identity{}
	}
	if c, err := r.Cookie(CookieName); err == nil {
		if op, ok := a.Valid(c.Value); ok {
			return Identity{Subject: op, Role: RoleAdmin, Method: "session"}
		}
	}
	return Identity{}
}

// Require wraps h so only callers with one of roles get through.
func (a *Auth) Require(h http.Handler, roles ...Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := a.Identify(r)
		if id.Role == RoleNone {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		for _, role := range roles {
			if id.Role == role {
				h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
				return
			}
		}
		writeErr(w, http.StatusForbidden, "insufficient role")
	})
}

type sessionRequest struct {
	Token    string `json:"token"`
	Remember bool   `json:"remember"`
	Operator string `json:"operator"`
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// Routes registers POST/DELETE /api/v1/session and GET /api/v1/whoami.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		var req sessionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if !a.Required() {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "open"})
			return
		}
		if !secureEq(a.key, []byte(req.Token)) {
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		ttl := SessionTTL
		if req.Remember {
			ttl = RememberTTL
		}
		v, exp := a.Mint(ttl, req.Operator)
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: v, Path: "/", Expires: exp,
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r),
		})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_at": exp, "operator": Operator(req.Operator)})
	})
	mux.HandleFunc("DELETE /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure(r),
		})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		id := a.Identify(r)
		if id.Role == RoleNone {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"identity": id, "auth_required": a.Required()})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
