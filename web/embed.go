// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package web embeds the decision pulse dashboard.
package web

import "embed"

//go:embed index.html
var FS embed.FS
