// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/auth"
)

// serviceTokenCmd creates a service credential. The token is printed once and
// never stored; the policy file keeps only its SHA-256.
func serviceTokenCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("service-token", flag.ContinueOnError)
	name := fs.String("name", "", "service name, e.g. gryvia-agent-helper")
	roles := fs.String("roles", "viewer", "roles, comma separated: viewer, proposer")
	tenant := fs.String("tenant", "", "confine the token to one tenant (default: deployment-wide)")
	days := fs.Int("days", 90, "valid for this many days (0 = no expiry)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tok, hash := auth.NewServiceToken()
	c := auth.ServiceCredential{Name: *name, TokenHash: hash, Tenant: *tenant}
	var names []string
	for _, r := range strings.Split(*roles, ",") {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		role, err := auth.ParseRole(r)
		if err != nil {
			return err
		}
		c.Roles = append(c.Roles, role)
		names = append(names, r)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	fmt.Fprintf(out, "token (shown once, the caller sends it as \"Authorization: Bearer <token>\"):\n  %s\n\n", tok)
	fmt.Fprintln(out, "add to the policy file:")
	fmt.Fprintln(out, "service_tokens:")
	fmt.Fprintf(out, "  - name: %s\n    token_sha256: %s\n    roles: [%s]\n", c.Name, hash, strings.Join(names, ", "))
	if c.Tenant != "" {
		fmt.Fprintf(out, "    tenant: %s\n", c.Tenant)
	}
	if *days > 0 {
		fmt.Fprintf(out, "    not_after: %s\n", time.Now().UTC().AddDate(0, 0, *days).Format(time.RFC3339))
	}
	fmt.Fprintln(out, "\nto rotate: add a second entry with the same name, then set not_after on the old one.")
	fmt.Fprintln(out, "to revoke: set revoked: true.")
	return nil
}
