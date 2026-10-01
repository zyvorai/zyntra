// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package httpsrc

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Field extracts a number from decoded JSON using a dot path.
//
//	"utilizationPercent"       object key
//	"items.0.status.cpu"       array index
//	"items.#"                  array length
//	"items.#(state=running)"   count of array elements whose key equals value
//	"fs.#(mount=/).used"       key of the first element whose key equals value
//	"items.*.cost"             sum of a key across array elements
//
// Booleans map to 1/0 and numeric strings are parsed.
func Field(v any, path string) (float64, error) {
	if path == "" {
		return toNumber(v)
	}
	parts := strings.Split(path, ".")
	for i, p := range parts {
		switch {
		case p == "#":
			arr, ok := v.([]any)
			if !ok {
				return 0, fmt.Errorf("field %q: %q is not an array", path, strings.Join(parts[:i], "."))
			}
			return float64(len(arr)), nil
		case strings.HasPrefix(p, "#(") && strings.HasSuffix(p, ")"):
			arr, ok := v.([]any)
			if !ok {
				return 0, fmt.Errorf("field %q: %q is not an array", path, strings.Join(parts[:i], "."))
			}
			k, want, ok := strings.Cut(p[2:len(p)-1], "=")
			if !ok {
				return 0, fmt.Errorf("field %q: filter needs key=value", path)
			}
			n := 0
			var first any
			for _, el := range arr {
				if m, ok := el.(map[string]any); ok && strings.EqualFold(fmt.Sprint(m[k]), want) {
					if n == 0 {
						first = el
					}
					n++
				}
			}
			if i == len(parts)-1 {
				return float64(n), nil
			}
			if first == nil {
				return 0, fmt.Errorf("field %q: no element with %s=%s", path, k, want)
			}
			return Field(first, strings.Join(parts[i+1:], "."))
		case p == "*":
			arr, ok := v.([]any)
			if !ok {
				return 0, fmt.Errorf("field %q: %q is not an array", path, strings.Join(parts[:i], "."))
			}
			rest := strings.Join(parts[i+1:], ".")
			var sum float64
			for _, el := range arr {
				x, err := Field(el, rest)
				if err != nil {
					continue
				}
				sum += x
			}
			return sum, nil
		}
		switch t := v.(type) {
		case map[string]any:
			next, ok := t[p]
			if !ok {
				return 0, fmt.Errorf("field %q: key %q not found", path, p)
			}
			v = next
		case []any:
			idx, err := strconv.Atoi(p)
			if err != nil || idx < 0 || idx >= len(t) {
				return 0, fmt.Errorf("field %q: bad index %q", path, p)
			}
			v = t[idx]
		default:
			return 0, fmt.Errorf("field %q: cannot descend into %T at %q", path, v, p)
		}
	}
	return toNumber(v)
}

func toNumber(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	case []any:
		return float64(len(t)), nil
	case nil:
		return 0, fmt.Errorf("value is null")
	}
	return 0, fmt.Errorf("value of type %T is not numeric", v)
}

// Sample is one line of the Prometheus text exposition format.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// ParseMetrics parses Prometheus text format. Comments and malformed lines
// are skipped.
func ParseMetrics(b []byte) []Sample {
	var out []Sample
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		s, ok := parseLine(line)
		if ok {
			out = append(out, s)
		}
	}
	return out
}

func parseLine(line string) (Sample, bool) {
	s := Sample{Labels: map[string]string{}}
	rest := line
	if i := strings.IndexAny(line, "{ \t"); i < 0 {
		return s, false
	} else {
		s.Name = line[:i]
		rest = line[i:]
	}
	if strings.HasPrefix(rest, "{") {
		end := labelsEnd(rest)
		if end < 0 {
			return s, false
		}
		parseLabels(rest[1:end], s.Labels)
		rest = rest[end+1:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return s, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return s, false
	}
	s.Value = v
	return s, true
}

func labelsEnd(s string) int {
	inQuote := false
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			inQuote = !inQuote
		case '}':
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

func parseLabels(s string, into map[string]string) {
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 || eq+1 >= len(s) || s[eq+1] != '"' {
			return
		}
		key := strings.TrimSpace(strings.TrimLeft(s[:eq], ", "))
		var val strings.Builder
		i := eq + 2
		for ; i < len(s); i++ {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					val.WriteByte('\n')
				default:
					val.WriteByte(s[i])
				}
				continue
			}
			if s[i] == '"' {
				break
			}
			val.WriteByte(s[i])
		}
		into[key] = val.String()
		if i+1 >= len(s) {
			return
		}
		s = s[i+1:]
	}
}

// Aggregate selects samples by name and label match and combines them with
// agg: sum (default), max, min, avg, count or first.
func Aggregate(samples []Sample, name string, labels map[string]string, agg string) (float64, error) {
	var vals []float64
	for _, s := range samples {
		if s.Name != name {
			continue
		}
		match := true
		for k, v := range labels {
			if s.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			vals = append(vals, s.Value)
		}
	}
	if len(vals) == 0 {
		return 0, fmt.Errorf("metric %s%s not found", name, fmtLabels(labels))
	}
	switch agg {
	case "", "sum":
		var t float64
		for _, v := range vals {
			t += v
		}
		return t, nil
	case "max":
		m := math.Inf(-1)
		for _, v := range vals {
			m = math.Max(m, v)
		}
		return m, nil
	case "min":
		m := math.Inf(1)
		for _, v := range vals {
			m = math.Min(m, v)
		}
		return m, nil
	case "avg":
		var t float64
		for _, v := range vals {
			t += v
		}
		return t / float64(len(vals)), nil
	case "count":
		return float64(len(vals)), nil
	case "first":
		return vals[0], nil
	}
	return 0, fmt.Errorf("unknown agg %q", agg)
}

func fmtLabels(l map[string]string) string {
	if len(l) == 0 {
		return ""
	}
	var parts []string
	for k, v := range l {
		parts = append(parts, k+"="+strconv.Quote(v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
