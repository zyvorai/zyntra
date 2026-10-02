// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package envx expands ${ZYNTRA_*} variables in pack files. Only variables
// with the ZYNTRA_ prefix are read, so a pack cannot pull unrelated secrets
// from the environment into a URL or header.
package envx

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// Prefix is the only environment prefix a pack may read.
const Prefix = "ZYNTRA_"

var ref = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// reserved are Zyntra's own credentials. A pack must never be able to put
// them in an outgoing URL, header or body.
var reserved = map[string]bool{
	"ZYNTRA_API_KEY":            true,
	"ZYNTRA_SESSION_SECRET":     true,
	"ZYNTRA_EXEC_TOKEN":         true,
	"ZYNTRA_INGEST_TOKEN":       true,
	"ZYNTRA_KEEP_TOKEN":         true,
	"ZYNTRA_OIDC_CLIENT_SECRET": true,
	"ZYNTRA_AI_API_KEY":         true,
	"ZYNTRA_FABRIC_PASSWORD":    true,
	"ZYNTRA_ADMIN_PASSWORD":     true,
}

// Allowed reports whether a pack may read the variable.
func Allowed(name string) bool { return strings.HasPrefix(name, Prefix) && !reserved[name] }

// Expand replaces ${ZYNTRA_*} references with environment values. It returns
// the names that were referenced but unset (or not allowed).
func Expand(s string) (string, []string) {
	var missing []string
	out := ref.ReplaceAllStringFunc(s, func(m string) string {
		name := ref.FindStringSubmatch(m)[1]
		if !Allowed(name) {
			missing = append(missing, name)
			return m
		}
		v, ok := os.LookupEnv(name)
		if !ok || v == "" {
			missing = append(missing, name)
			return m
		}
		return v
	})
	return out, missing
}

// Refs returns the variable names referenced in s, sorted and unique.
func Refs(s ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		for _, m := range ref.FindAllStringSubmatch(x, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	sort.Strings(out)
	return out
}
