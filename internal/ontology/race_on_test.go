// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

//go:build race

package ontology

// The race detector slows code by roughly 5-20x, so wall-clock limits in the
// scale tests are widened to still catch a quadratic regression (which would
// miss by orders of magnitude) without failing on instrumentation cost.
const slowdown = 20
