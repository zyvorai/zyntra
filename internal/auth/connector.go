// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// TokenPrefix marks connector tokens so they are easy to recognise in logs
// and secret scanners.
const TokenPrefix = "zct_"

var connectorName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// IngestCredential is one connector's token. Only the SHA-256 of the token is
// kept, so the policy file holds nothing that can be replayed. Rotate by
// adding a second credential with the same name and a NotAfter on the old
// one; revoke by setting Revoked.
type IngestCredential struct {
	Name      string
	TokenHash string // hex SHA-256 of the token
	Tenants   []string
	Types     []string
	NotBefore time.Time
	NotAfter  time.Time
	Revoked   bool
}

// Validate checks one credential's shape.
func (c IngestCredential) Validate() error {
	switch {
	case !connectorName.MatchString(c.Name):
		return fmt.Errorf("connector name %q must be lowercase letters, digits, - or _", c.Name)
	case len(c.TokenHash) != 64:
		return fmt.Errorf("connector %s: token_sha256 must be 64 hex characters", c.Name)
	case len(c.Tenants) == 0:
		return fmt.Errorf("connector %s: list the tenants it may write to (or \"default\")", c.Name)
	}
	if _, err := hex.DecodeString(c.TokenHash); err != nil {
		return fmt.Errorf("connector %s: token_sha256 is not hex", c.Name)
	}
	for _, t := range c.Tenants {
		if t != "default" && !TenantPattern.MatchString(t) {
			return fmt.Errorf("connector %s: invalid tenant %q", c.Name, t)
		}
	}
	if !c.NotAfter.IsZero() && !c.NotBefore.IsZero() && !c.NotAfter.After(c.NotBefore) {
		return fmt.Errorf("connector %s: not_after must be after not_before", c.Name)
	}
	return nil
}

// HashToken returns the hex SHA-256 stored for a token.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// NewConnectorToken returns a fresh random token and its hash.
func NewConnectorToken() (token, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	token = TokenPrefix + hex.EncodeToString(b[:])
	return token, HashToken(token)
}

// SetCredentials installs the connector credentials.
func (a *Auth) SetCredentials(cs []IngestCredential) error {
	for _, c := range cs {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	a.creds = cs
	return nil
}

// Credentials returns the installed credentials (hashes only), for status.
func (a *Auth) Credentials() []IngestCredential { return append([]IngestCredential(nil), a.creds...) }

// connector resolves a bearer token to a connector identity. Every
// credential is compared, so timing does not reveal which one matched.
func (a *Auth) connector(tok string) (Identity, bool) {
	if !strings.HasPrefix(tok, TokenPrefix) || len(a.creds) == 0 {
		return Identity{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	now := a.now()
	var found *IngestCredential
	for i := range a.creds {
		want, err := hex.DecodeString(a.creds[i].TokenHash)
		if err == nil && subtle.ConstantTimeCompare(sum[:], want) == 1 {
			found = &a.creds[i]
		}
	}
	if found == nil || found.Revoked ||
		(!found.NotBefore.IsZero() && now.Before(found.NotBefore)) ||
		(!found.NotAfter.IsZero() && !now.Before(found.NotAfter)) {
		return Identity{}, false
	}
	id := newIdentity("connector:"+found.Name, "connector-credential", RoleIngest)
	id.Scope = &IngestScope{Connector: found.Name, Tenants: found.Tenants, Types: found.Types}
	return id, true
}
