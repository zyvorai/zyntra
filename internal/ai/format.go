// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func fmtFloat(v float64) string {
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func fmtPct(x float64) string {
	return fmt.Sprintf("%+.1f%%", x*100)
}

func unitSuffix(u string) string {
	switch u {
	case "":
		return ""
	case "%":
		return "%"
	}
	return " " + u
}

func fmtDur(sec float64) string {
	sec = math.Abs(sec)
	switch {
	case sec < 90:
		return fmt.Sprintf("%.0fs", sec)
	case sec < 90*60:
		return fmt.Sprintf("%.0f min", sec/60)
	case sec < 48*3600:
		return fmt.Sprintf("%.1f h", sec/3600)
	}
	return fmt.Sprintf("%.1f days", sec/86400)
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
