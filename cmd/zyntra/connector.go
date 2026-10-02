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

// connectorTokenCmd creates a connector credential. The token is printed once
// and never stored; the policy file keeps only its SHA-256.
func connectorTokenCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("connector-token", flag.ContinueOnError)
	name := fs.String("name", "", "connector name, e.g. mes-alpha")
	tenants := fs.String("tenant", "", "tenants it may write to, comma separated (\"default\" for the tenant-less space)")
	types := fs.String("types", "", "object types it may write, comma separated (default: any)")
	days := fs.Int("days", 90, "valid for this many days (0 = no expiry)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	split := func(s string) []string {
		var o []string
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				o = append(o, p)
			}
		}
		return o
	}
	tok, hash := auth.NewConnectorToken()
	c := auth.IngestCredential{Name: *name, TokenHash: hash, Tenants: split(*tenants), Types: split(*types)}
	if err := c.Validate(); err != nil {
		return err
	}
	fmt.Fprintf(out, "token (shown once, give it to the connector as ZYNTRA_INGEST_TOKEN):\n  %s\n\n", tok)
	fmt.Fprintln(out, "add to the policy file:")
	fmt.Fprintln(out, "connectors:")
	fmt.Fprintf(out, "  - name: %s\n    token_sha256: %s\n    tenants: [%s]\n", c.Name, hash, strings.Join(c.Tenants, ", "))
	if len(c.Types) > 0 {
		fmt.Fprintf(out, "    object_types: [%s]\n", strings.Join(c.Types, ", "))
	}
	if *days > 0 {
		fmt.Fprintf(out, "    not_after: %s\n", time.Now().UTC().AddDate(0, 0, *days).Format(time.RFC3339))
	}
	fmt.Fprintln(out, "\nto rotate: add a second entry with the same name, then set not_after on the old one.")
	fmt.Fprintln(out, "to revoke: set revoked: true.")
	return nil
}
