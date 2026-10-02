// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ServiceTokenPrefix marks service tokens, as TokenPrefix marks connector
// tokens.
const ServiceTokenPrefix = "zst_"

// ServiceRoles are the roles a service token may carry. A service can read
// and propose; approving, executing and administering stay with people.
var ServiceRoles = []Role{RoleViewer, RoleProposer}

// ServiceCredential is one machine caller's token, for example an agent that
// reads objects and drafts proposals. Only the SHA-256 of the token is kept.
// Rotate and revoke as for connector credentials.
type ServiceCredential struct {
	Name      string
	TokenHash string // hex SHA-256 of the token
	Roles     []Role
	// Tenant confines the token to one tenant's workspace, as for a
	// tenant-bound user.
	Tenant    string
	NotBefore time.Time
	NotAfter  time.Time
	Revoked   bool
}

// Validate checks one credential's shape.
func (c ServiceCredential) Validate() error {
	switch {
	case !connectorName.MatchString(c.Name):
		return fmt.Errorf("service name %q must be lowercase letters, digits, - or _", c.Name)
	case len(c.TokenHash) != 64:
		return fmt.Errorf("service %s: token_sha256 must be 64 hex characters", c.Name)
	case len(c.Roles) == 0:
		return fmt.Errorf("service %s: list its roles (viewer, proposer)", c.Name)
	}
	if _, err := hex.DecodeString(c.TokenHash); err != nil {
		return fmt.Errorf("service %s: token_sha256 is not hex", c.Name)
	}
	for _, r := range c.Roles {
		if !slices.Contains(ServiceRoles, r) {
			return fmt.Errorf("service %s: role %q is not allowed for a service token (allowed: viewer, proposer)", c.Name, r)
		}
	}
	if c.Tenant != "" && !TenantPattern.MatchString(c.Tenant) {
		return fmt.Errorf("service %s: invalid tenant %q", c.Name, c.Tenant)
	}
	if !c.NotAfter.IsZero() && !c.NotBefore.IsZero() && !c.NotAfter.After(c.NotBefore) {
		return fmt.Errorf("service %s: not_after must be after not_before", c.Name)
	}
	return nil
}

// NewServiceToken returns a fresh random service token and its hash.
func NewServiceToken() (token, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	token = ServiceTokenPrefix + hex.EncodeToString(b[:])
	return token, HashToken(token)
}

// SetServiceTokens installs the service credentials.
func (a *Auth) SetServiceTokens(cs []ServiceCredential) error {
	for _, c := range cs {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	a.services = cs
	return nil
}

// ServiceTokens returns the installed credentials (hashes only), for status.
func (a *Auth) ServiceTokens() []ServiceCredential {
	return append([]ServiceCredential(nil), a.services...)
}

// service resolves a bearer token to a service identity. Every credential is
// compared, so timing does not reveal which one matched.
func (a *Auth) service(tok string) (Identity, bool) {
	if !strings.HasPrefix(tok, ServiceTokenPrefix) || len(a.services) == 0 {
		return Identity{}, false
	}
	sum := sha256.Sum256([]byte(tok))
	now := a.now()
	var found *ServiceCredential
	for i := range a.services {
		want, err := hex.DecodeString(a.services[i].TokenHash)
		if err == nil && subtle.ConstantTimeCompare(sum[:], want) == 1 {
			found = &a.services[i]
		}
	}
	if found == nil || found.Revoked ||
		(!found.NotBefore.IsZero() && now.Before(found.NotBefore)) ||
		(!found.NotAfter.IsZero() && !now.Before(found.NotAfter)) {
		return Identity{}, false
	}
	id := newIdentity("service:"+found.Name, "service-token", found.Roles...)
	if found.Tenant != "" {
		var err error
		if id, err = id.WithTenant(found.Tenant); err != nil {
			return Identity{}, false
		}
	}
	return id, id.Role != RoleNone
}
