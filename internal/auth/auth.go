// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package auth identifies API callers and assigns roles. Callers sign in
// with OpenID Connect, a local account from the policy file, or the shared
// admin key (kept as break-glass access); the UI then holds an HMAC-signed
// HttpOnly session cookie that carries the subject and roles. With nothing
// configured the API is open (development mode).
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName  = "zyntra_session"
	SessionTTL  = 12 * time.Hour
	RememberTTL = 7 * 24 * time.Hour
)

type Role string

const (
	RoleNone Role = ""
	// RoleViewer reads models, plans, proposals and audit.
	RoleViewer Role = "viewer"
	// RoleProposer may also create proposals.
	RoleProposer Role = "proposer"
	// RoleApprover may also approve and reject proposals.
	RoleApprover Role = "approver"
	// RoleExecutor may run approved proposals.
	RoleExecutor Role = "executor"
	// RoleAdmin may do everything.
	RoleAdmin Role = "admin"
	// RoleExec may only call the execution endpoint (Keep's broker uses it).
	RoleExec Role = "exec"
	// RoleIngest may only post documents to webhook-in channels.
	RoleIngest Role = "ingest"
)

// Route groups.
var (
	Readers   = []Role{RoleViewer, RoleProposer, RoleApprover, RoleExecutor, RoleAdmin}
	Proposers = []Role{RoleProposer, RoleApprover, RoleAdmin}
	Approvers = []Role{RoleApprover, RoleAdmin}
	Executors = []Role{RoleExecutor, RoleAdmin, RoleExec}
	Admins    = []Role{RoleAdmin}
	Ingesters = []Role{RoleIngest, RoleAdmin}
)

var rank = map[Role]int{RoleViewer: 1, RoleExec: 1, RoleIngest: 1, RoleProposer: 2, RoleExecutor: 3, RoleApprover: 4, RoleAdmin: 5}

// ParseRole accepts a user-facing role name.
func ParseRole(s string) (Role, error) {
	r := Role(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := rank[r]; !ok || r == RoleExec || r == RoleIngest {
		return RoleNone, fmt.Errorf("unknown role %q (viewer, proposer, approver, executor, admin)", s)
	}
	return r, nil
}

type Identity struct {
	Subject string `json:"subject"`
	// Role is the most powerful of Roles, kept for older clients.
	Role   Role   `json:"role"`
	Roles  []Role `json:"roles"`
	Method string `json:"method"`
}

func newIdentity(subject, method string, roles ...Role) Identity {
	roles = normalize(roles)
	id := Identity{Subject: subject, Method: method, Roles: roles}
	for _, r := range roles {
		if rank[r] > rank[id.Role] {
			id.Role = r
		}
	}
	return id
}

func normalize(roles []Role) []Role {
	seen := map[Role]bool{}
	var out []Role
	for _, r := range roles {
		if _, ok := rank[r]; ok && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return rank[out[i]] < rank[out[j]] })
	return out
}

// Has reports whether the identity holds any of roles.
func (id Identity) Has(roles ...Role) bool {
	for _, have := range id.Roles {
		if slices.Contains(roles, have) {
			return true
		}
	}
	return false
}

type ctxKey struct{}

func FromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(ctxKey{}).(Identity)
	return id
}

// LocalUser is an account checked against a bcrypt hash. Default marks the
// built-in admin whose password is still the shipped one.
type LocalUser struct {
	Name    string
	Hash    string
	Roles   []Role
	Default bool
}

type Auth struct {
	key       []byte
	execKey   []byte
	ingestKey []byte
	secret    []byte
	users     map[string]LocalUser
	oidc      *oidcState
	now       func() time.Time
}

// New returns an Auth. With no apiKey, local users or OIDC the API is open.
func New(apiKey, execKey string) *Auth {
	a := &Auth{key: []byte(apiKey), execKey: []byte(execKey), now: time.Now}
	if apiKey != "" {
		a.secret = []byte(apiKey)
	} else {
		a.secret = make([]byte, 32)
		_, _ = rand.Read(a.secret)
	}
	return a
}

// SetSessionSecret sets the cookie signing key, so sessions survive
// restarts and rotating the admin key does not log everyone out.
func (a *Auth) SetSessionSecret(s string) {
	if s != "" {
		a.secret = []byte(s)
	}
}

// SetIngestToken sets the bearer token gateways use to post to webhook-in
// channels. It grants nothing else.
func (a *Auth) SetIngestToken(s string) { a.ingestKey = []byte(s) }

// SetUsers enables local accounts.
func (a *Auth) SetUsers(users []LocalUser) {
	a.users = map[string]LocalUser{}
	for _, u := range users {
		a.users[u.Name] = u
	}
}

func (a *Auth) Required() bool { return len(a.key) > 0 || len(a.users) > 0 || a.oidc != nil }

// Methods lists the sign-in methods offered.
func (a *Auth) Methods() []string {
	var m []string
	if a.oidc != nil {
		m = append(m, "oidc")
	}
	if len(a.users) > 0 {
		m = append(m, "password")
	}
	if len(a.key) > 0 {
		m = append(m, "key")
	}
	return m
}

func secureEq(a, b []byte) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare(a, b) == 1
}

func (a *Auth) mac(parts ...string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(m.Sum(nil))
}

func (a *Auth) sign(exp int64, method, roles, operator string) string {
	return a.mac("zyntra-session.v2", strconv.FormatInt(exp, 10), method, roles, operator)
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

func joinRoles(roles []Role) string {
	s := make([]string, len(roles))
	for i, r := range roles {
		s[i] = string(r)
	}
	return strings.Join(s, "+")
}

// Mint returns a cookie value for an identity valid for ttl. The format is
// exp.method.roles.operator.sig; operator may itself contain dots.
func (a *Auth) Mint(ttl time.Duration, id Identity) (string, time.Time) {
	op := Operator(id.Subject)
	roles := joinRoles(normalize(id.Roles))
	method := id.Method
	if method == "" {
		method = "session"
	}
	exp := a.now().Add(ttl)
	e := exp.Unix()
	return strings.Join([]string{strconv.FormatInt(e, 10), method, roles, op, a.sign(e, method, roles, op)}, "."), exp
}

// Valid returns the identity in an unexpired, correctly signed cookie.
func (a *Auth) Valid(v string) (Identity, bool) {
	if !a.Required() {
		return Identity{}, false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 5 {
		return Identity{}, false
	}
	expStr, method, roles, sig := parts[0], parts[1], parts[2], parts[len(parts)-1]
	op := strings.Join(parts[3:len(parts)-1], ".")
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || a.now().Unix() >= exp || Operator(op) != op {
		return Identity{}, false
	}
	if !hmac.Equal([]byte(sig), []byte(a.sign(exp, method, roles, op))) {
		return Identity{}, false
	}
	var rs []Role
	for r := range strings.SplitSeq(roles, "+") {
		rs = append(rs, Role(r))
	}
	id := newIdentity(op, method, rs...)
	return id, id.Role != RoleNone
}

// Identify resolves the caller from a Bearer token or session cookie.
func (a *Auth) Identify(r *http.Request) Identity {
	if !a.Required() {
		return newIdentity("anonymous", "open", RoleAdmin)
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		tok = strings.TrimSpace(tok)
		switch {
		case secureEq(a.key, []byte(tok)):
			return newIdentity("admin", "bearer", RoleAdmin)
		case secureEq(a.execKey, []byte(tok)):
			return newIdentity("keep-broker", "exec-token", RoleExec)
		case secureEq(a.ingestKey, []byte(tok)):
			return newIdentity("ingest", "ingest-token", RoleIngest)
		}
		return Identity{}
	}
	if c, err := r.Cookie(CookieName); err == nil {
		if id, ok := a.Valid(c.Value); ok {
			return id
		}
	}
	return Identity{}
}

// Require wraps h so only callers holding one of roles get through.
func (a *Auth) Require(h http.Handler, roles ...Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := a.Identify(r)
		if id.Role == RoleNone {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if !id.Has(roles...) {
			writeErr(w, http.StatusForbidden, "insufficient role")
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

type sessionRequest struct {
	Token    string `json:"token"`
	Remember bool   `json:"remember"`
	Operator string `json:"operator"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// dummyHash keeps password checks for unknown users as slow as for known
// ones.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("zyntra-dummy"), bcrypt.DefaultCost)

func (a *Auth) checkPassword(name, password string) (LocalUser, bool) {
	u, ok := a.users[name]
	hash := []byte(u.Hash)
	if !ok {
		hash = dummyHash
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || !ok {
		return LocalUser{}, false
	}
	return u, true
}

func (a *Auth) setSession(w http.ResponseWriter, r *http.Request, id Identity, ttl time.Duration) time.Time {
	v, exp := a.Mint(ttl, id)
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: v, Path: "/", Expires: exp,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r),
	})
	return exp
}

// Routes registers the session, whoami and (when enabled) OIDC routes.
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
		var id Identity
		switch {
		case req.Token != "" && secureEq(a.key, []byte(req.Token)):
			id = newIdentity(Operator(req.Operator), "key", RoleAdmin)
		case req.Username != "" && len(a.users) > 0:
			u, ok := a.checkPassword(req.Username, req.Password)
			if !ok {
				writeErr(w, http.StatusUnauthorized, "invalid credentials")
				return
			}
			id = newIdentity(Operator(u.Name), "password", u.Roles...)
		default:
			writeErr(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		ttl := SessionTTL
		if req.Remember {
			ttl = RememberTTL
		}
		exp := a.setSession(w, r, id, ttl)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_at": exp, "operator": id.Subject, "identity": id})
	})
	mux.HandleFunc("DELETE /api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name: CookieName, Value: "", Path: "/", MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure(r),
		})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		id := a.Identify(r)
		if id.Role == RoleNone {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		u, ok := a.users[id.Subject]
		writeJSON(w, http.StatusOK, map[string]any{"identity": id, "auth_required": a.Required(), "methods": a.Methods(),
			"default_password": ok && u.Default && id.Method == "password"})
	})
	if a.oidc != nil {
		mux.HandleFunc("GET /api/v1/auth/oidc/login", a.oidcLogin)
		mux.HandleFunc("GET /api/v1/auth/oidc/callback", a.oidcCallback)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
