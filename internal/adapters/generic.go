// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package adapters

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zyvorai/zyntra/internal/adapters/httpsrc"
	"github.com/zyvorai/zyntra/internal/envx"
	"github.com/zyvorai/zyntra/internal/graph"
)

// Source health states.
const (
	StateOK       = "ok"
	StateStale    = "stale"
	StateError    = "error"
	StateFallback = "fallback"
)

const maxDoc = 16 << 20

// promDoc is a parsed Prometheus text document.
type promDoc []httpsrc.Sample

// FileCache parses files once and re-reads them when their modification
// time or size changes.
type FileCache struct {
	mu    sync.Mutex
	files map[string]cachedFile
}

type cachedFile struct {
	mod  time.Time
	size int64
	doc  any
	err  error
}

func NewFileCache() *FileCache { return &FileCache{files: map[string]cachedFile{}} }

// Load returns the parsed document at path.
func (c *FileCache) Load(path, format string) (any, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.Size() > maxDoc {
		return nil, fmt.Errorf("%s is larger than %d MiB", path, maxDoc>>20)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := path + "\x00" + format
	if f, ok := c.files[key]; ok && f.mod.Equal(fi.ModTime()) && f.size == fi.Size() {
		return f.doc, f.err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, perr := Parse(formatFor(format, path, ""), b)
	c.files[key] = cachedFile{mod: fi.ModTime(), size: fi.Size(), doc: doc, err: perr}
	return doc, perr
}

func formatFor(format, path, contentType string) string {
	if format != "" {
		return format
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		return "csv"
	case ".yaml", ".yml":
		return "yaml"
	case ".prom":
		return "prometheus"
	case ".json":
		return "json"
	}
	switch ct := strings.ToLower(contentType); {
	case strings.Contains(ct, "csv"):
		return "csv"
	case strings.Contains(ct, "yaml"):
		return "yaml"
	case strings.HasPrefix(ct, "text/plain"):
		return "prometheus"
	}
	return "json"
}

// Parse decodes a document. CSV becomes an array of row objects keyed by
// the header, with numeric cells as numbers.
func Parse(format string, b []byte) (any, error) {
	switch format {
	case "csv":
		return parseCSV(b)
	case "yaml":
		var v any
		if err := yaml.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("yaml: %w", err)
		}
		return normalize(v), nil
	case "prometheus":
		return promDoc(httpsrc.ParseMetrics(b)), nil
	case "", "json":
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("json: %w", err)
		}
		return v, nil
	}
	return nil, fmt.Errorf("unknown format %q", format)
}

func parseCSV(b []byte) (any, error) {
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	recs, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	if len(recs) == 0 {
		return []any{}, nil
	}
	head := recs[0]
	for i := range head {
		head[i] = strings.TrimSpace(head[i])
	}
	rows := make([]any, 0, len(recs)-1)
	for _, rec := range recs[1:] {
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		row := make(map[string]any, len(head))
		for i, h := range head {
			if i >= len(rec) || h == "" {
				continue
			}
			row[h] = cell(rec[i])
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// cell turns "1,20,000" or "42.5" into a number and leaves text alone.
func cell(s string) any {
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	if strings.ContainsAny(s, "0123456789") {
		if f, err := strconv.ParseFloat(strings.NewReplacer(",", "", "_", "").Replace(s), 64); err == nil {
			return f
		}
	}
	return s
}

// normalize converts YAML integers to float64 so documents look like JSON.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	}
	return v
}

// Extract reads a KPI value from a parsed document.
func Extract(doc any, s graph.Source) (float64, error) {
	if samples, ok := doc.(promDoc); ok {
		return httpsrc.Aggregate(samples, s.Metric, s.Labels, s.Agg)
	}
	doc = where(doc, s.Where)
	v, err := pick(doc, s.Field, s.Agg)
	if err != nil {
		return 0, err
	}
	if s.Denominator != "" {
		d, err := pick(doc, s.Denominator, "")
		if err != nil {
			return 0, fmt.Errorf("denominator: %w", err)
		}
		if d == 0 {
			return 0, fmt.Errorf("denominator %s is zero", s.Denominator)
		}
		v /= d
	}
	return v, nil
}

func pick(doc any, field, agg string) (float64, error) {
	if agg != "" && strings.Contains(field, "*") {
		vals, err := httpsrc.Values(doc, field)
		if err != nil {
			return 0, err
		}
		return httpsrc.Combine(vals, agg)
	}
	return httpsrc.Field(doc, field)
}

func where(doc any, w map[string]string) any {
	rows, ok := doc.([]any)
	if !ok || len(w) == 0 {
		return doc
	}
	out := make([]any, 0, len(rows))
	for _, el := range rows {
		m, ok := el.(map[string]any)
		if !ok {
			continue
		}
		match := true
		for k, want := range w {
			if !strings.EqualFold(fmt.Sprint(m[k]), want) {
				match = false
				break
			}
		}
		if match {
			out = append(out, el)
		}
	}
	return out
}

// HoldTracker remembers which KPIs have had a sample inside their calendar
// window, so values outside it can be held instead of replaced.
type HoldTracker struct {
	mu   sync.Mutex
	seen map[string]bool
}

func NewHoldTracker() *HoldTracker { return &HoldTracker{seen: map[string]bool{}} }

func (h *HoldTracker) mark(id string) {
	h.mu.Lock()
	h.seen[id] = true
	h.mu.Unlock()
}

func (h *HoldTracker) has(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seen[id]
}

// FilePath resolves a file source against the model directory.
func FilePath(m *graph.Model, file string) string {
	if filepath.IsAbs(file) || m.Dir == "" {
		return file
	}
	return filepath.Join(m.Dir, file)
}

// generic reads a value from a generic source. served is false when the
// config cannot serve it (no inputs store, for example).
func (r *run) generic(ctx context.Context, m *graph.Model, k *graph.KPI) (v float64, served bool, err error) {
	s := *k.Source
	now := r.cfg.now()
	switch s.Kind {
	case graph.SourceFile:
		files := r.cfg.Files
		if files == nil {
			files = NewFileCache()
		}
		path := FilePath(m, s.File)
		start := time.Now()
		doc, err := files.Load(path, s.Format)
		r.record("file:"+s.File, s.Kind, start, err)
		if err != nil {
			return 0, true, err
		}
		v, err := Extract(doc, s)
		return v, true, err
	case graph.SourceHTTP, graph.SourceSheet:
		u, missing := envx.Expand(s.URL)
		name := s.URL
		if len(missing) > 0 {
			r.recordState(name, s.Kind, StateFallback, "not configured: set "+strings.Join(missing, ", "))
			return 0, false, nil
		}
		key := s.Kind + " " + u
		d, ok := r.docs[key]
		if !ok {
			start := time.Now()
			d.v, d.err = r.fetch(ctx, u, s)
			r.docs[key] = d
			r.record(name, s.Kind, start, d.err)
		}
		if d.err != nil {
			return 0, true, d.err
		}
		v, err := Extract(d.v, s)
		return v, true, err
	case graph.SourceWebhookIn:
		name := "webhook:" + s.Name
		if r.cfg.Inputs == nil {
			r.recordState(name, s.Kind, StateFallback, "webhook-in needs zyntra serve")
			return 0, false, nil
		}
		doc, ok := r.cfg.Inputs.Webhook(s.Name)
		if !ok {
			r.recordState(name, s.Kind, StateFallback, "nothing received yet on /api/v1/ingest/"+s.Name)
			return 0, true, fmt.Errorf("nothing received on webhook %s yet", s.Name)
		}
		if s.StaleAfter > 0 && now.Sub(doc.At) > s.StaleAfter.D() {
			msg := fmt.Sprintf("last document %s ago (stale after %s)", now.Sub(doc.At).Round(time.Second), s.StaleAfter.D())
			r.recordState(name, s.Kind, StateStale, msg)
			return 0, true, fmt.Errorf("webhook %s: %s", s.Name, msg)
		}
		r.recordState(name, s.Kind, StateOK, "")
		v, err := Extract(doc.Body, s)
		return v, true, err
	case graph.SourceManual:
		if r.cfg.Inputs == nil {
			r.recordState("manual", s.Kind, StateFallback, "manual values need zyntra serve or a state directory")
			return 0, false, nil
		}
		mv, ok := r.cfg.Inputs.Manual(k.ID)
		if !ok {
			r.recordState("manual", s.Kind, StateFallback, "")
			return 0, true, fmt.Errorf("no value entered for %s yet", k.ID)
		}
		if s.StaleAfter > 0 && now.Sub(mv.At) > s.StaleAfter.D() {
			r.recordState("manual", s.Kind, StateStale, "")
			return 0, true, fmt.Errorf("manual value for %s is %s old (stale after %s)", k.ID, now.Sub(mv.At).Round(time.Second), s.StaleAfter.D())
		}
		r.recordState("manual", s.Kind, StateOK, "")
		return mv.Value, true, nil
	}
	return 0, true, fmt.Errorf("unknown source kind %q", s.Kind)
}

func (r *run) fetch(ctx context.Context, u string, s graph.Source) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	for h, v := range s.Headers {
		val, missing := envx.Expand(v)
		if len(missing) > 0 {
			return nil, fmt.Errorf("header %s: set %s", h, strings.Join(missing, ", "))
		}
		req.Header.Set(h, val)
	}
	req.Header.Set("Accept", "application/json, text/csv;q=0.9, text/plain;q=0.8")
	c := r.cfg.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s: %w", req.URL.Host, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDoc))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	format := s.Format
	if s.Kind == graph.SourceSheet && format == "" {
		format = "csv"
	}
	return Parse(formatFor(format, "", resp.Header.Get("Content-Type")), b)
}

// FormatOf guesses a document format from a file name.
func FormatOf(path string) string { return formatFor("", path, "") }

// Row is one record behind a KPI, with its position in the source file.
type Row struct {
	Source string         `json:"source"`
	Index  int            `json:"index"`
	Values map[string]any `json:"values"`
}

var countMatch = regexp.MustCompile(`^#\(([^=()]+)=([^()]*)\)`)

// Rows returns the records a KPI's file source reads: the rows its where
// filter keeps, narrowed by a "#(k=v)" field when it counts matches. Other
// source kinds have no rows.
func Rows(m *graph.Model, k graph.KPI, files *FileCache) ([]Row, error) {
	if k.Source == nil || k.Source.Kind != graph.SourceFile {
		return nil, nil
	}
	if files == nil {
		files = NewFileCache()
	}
	doc, err := files.Load(FilePath(m, k.Source.File), k.Source.Format)
	if err != nil {
		return nil, err
	}
	all, ok := doc.([]any)
	if !ok {
		return nil, nil
	}
	match := map[string]string{}
	maps.Copy(match, k.Source.Where)
	if g := countMatch.FindStringSubmatch(k.Source.Field); g != nil {
		match[g[1]] = g[2]
	}
	var out []Row
	for i, el := range all {
		row, ok := el.(map[string]any)
		if !ok {
			continue
		}
		keep := true
		for col, want := range match {
			if !strings.EqualFold(fmt.Sprint(row[col]), want) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, Row{Source: k.Source.File, Index: i + 1, Values: row})
		}
	}
	return out, nil
}
