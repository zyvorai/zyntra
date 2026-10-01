// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package keeppack embeds the zyntra-executor Fabric Keep agent pack.
package keeppack

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed zyntra-executor/pack.json zyntra-executor/agent.mjs zyntra-executor/credential.json
var files embed.FS

type Pack struct {
	Name     string          `json:"name"`
	Entry    string          `json:"entry"`
	Manifest json.RawMessage `json:"manifest"`
	Bundle   []byte          `json:"-"`
	// Credential is the descriptor the Fabric host must list in
	// ZYVOR_AGENT_CREDENTIALS_FILE before the agent can be deployed.
	Credential json.RawMessage `json:"-"`
}

// Executor returns the embedded zyntra-executor pack.
func Executor() (Pack, error) {
	var p Pack
	b, err := files.ReadFile("zyntra-executor/pack.json")
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("pack.json: %w", err)
	}
	if p.Bundle, err = files.ReadFile("zyntra-executor/" + p.Entry); err != nil {
		return p, err
	}
	if p.Credential, err = files.ReadFile("zyntra-executor/credential.json"); err != nil {
		return p, err
	}
	return p, nil
}
