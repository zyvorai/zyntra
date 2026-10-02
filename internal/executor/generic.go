// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/zyvorai/zyntra/internal/envx"
	"github.com/zyvorai/zyntra/internal/gaps"
	"github.com/zyvorai/zyntra/internal/graph"
)

// now is the clock used in file templates (tests replace it).
var now = time.Now

const maxResponse = 1 << 20

// gapView is what "gap:ID" expands to in a webhook body.
type gapView struct {
	KPI       string  `json:"kpi"`
	Name      string  `json:"name"`
	Owner     string  `json:"owner,omitempty"`
	Unit      string  `json:"unit,omitempty"`
	Value     float64 `json:"value"`
	Target    float64 `json:"target"`
	Direction string  `json:"direction,omitempty"`
	Severity  float64 `json:"severity"`
}

func viewOf(k graph.KPI) gapView {
	g := gapView{KPI: k.ID, Name: k.Name, Owner: k.Owner, Unit: k.DisplayUnit(), Value: k.Value,
		Direction: string(k.Direction), Severity: math.Round(gaps.Severity(k, k.Value)*1e6) / 1e6}
	if k.Target != nil {
		g.Target = *k.Target
	}
	return g
}

// resolve replaces "kpi:ID" and "gap:ID" strings in a webhook body.
func resolve(m *graph.Model, v any) any {
	switch t := v.(type) {
	case string:
		if m == nil {
			return t
		}
		if id, ok := strings.CutPrefix(t, "kpi:"); ok {
			if k, found := m.KPI(id); found {
				return k.Value
			}
		}
		if id, ok := strings.CutPrefix(t, "gap:"); ok {
			if k, found := m.KPI(id); found {
				return viewOf(*k)
			}
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = resolve(m, x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = resolve(m, x)
		}
		return out
	}
	return v
}

func sortedKeys(h map[string]string) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func renderWebhook(m *graph.Model, a graph.Action) (Rendered, error) {
	w := a.Webhook
	method := strings.ToUpper(w.Method)
	if method == "" {
		method = http.MethodPost
	}
	r := Rendered{Template: graph.KindWebhook, Method: method, URL: w.URL, Headers: map[string]string{}}
	maps.Copy(r.Headers, w.Headers)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", method, w.URL)
	if w.Body != nil && method != http.MethodGet {
		body, err := json.MarshalIndent(resolve(m, w.Body), "", "  ")
		if err != nil {
			return r, fmt.Errorf("webhook body: %w", err)
		}
		r.Body = string(body)
		if _, ok := r.Headers["Content-Type"]; !ok {
			r.Headers["Content-Type"] = "application/json"
		}
	}
	for _, k := range sortedKeys(r.Headers) {
		fmt.Fprintf(&b, "%s: %s\n", k, r.Headers[k])
	}
	if r.Body != "" {
		b.WriteString("\n" + r.Body + "\n")
	}
	if refs := envx.Refs(append([]string{w.URL}, values(w.Headers)...)...); len(refs) > 0 {
		var unset []string
		for _, name := range refs {
			if _, missing := envx.Expand("${" + name + "}"); len(missing) > 0 {
				unset = append(unset, name)
			}
		}
		if len(unset) > 0 {
			fmt.Fprintf(&b, "\n# not set here: %s (needed to apply)\n", strings.Join(unset, ", "))
		}
	}
	r.Display = b.String()
	r.Args = []string{method, w.URL}
	return r, nil
}

func values(h map[string]string) []string {
	out := make([]string, 0, len(h))
	for _, v := range h {
		out = append(out, v)
	}
	return out
}

// fileContext is the data file templates see.
type fileContext struct {
	Action graph.Action
	KPIs   map[string]gapView
	Gaps   []gaps.Gap
	Date   string
	Time   string
	Stamp  string
}

var funcs = template.FuncMap{
	"pct": func(v float64) string { return fmt.Sprintf("%.1f%%", v*100) },
	"num": func(v float64) string { return fmt.Sprintf("%.4g", v) },
}

func execTemplate(name, text string, data any) (string, error) {
	t, err := template.New(name).Funcs(funcs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func renderFile(m *graph.Model, a graph.Action) (Rendered, error) {
	t := now()
	ctx := fileContext{Action: a, KPIs: map[string]gapView{}, Date: t.Format("2006-01-02"), Time: t.Format("15:04"), Stamp: t.Format("20060102-150405")}
	if m != nil {
		for _, k := range m.KPIs {
			ctx.KPIs[k.ID] = viewOf(k)
		}
		ctx.Gaps = gaps.Detect(m)
	}
	r := Rendered{Template: graph.KindFile}
	path, err := execTemplate("path", a.File.Path, ctx)
	if err != nil {
		return r, fmt.Errorf("file path: %w", err)
	}
	if err := safeRel(path); err != nil {
		return r, err
	}
	content, err := execTemplate("content", a.File.Content, ctx)
	if err != nil {
		return r, fmt.Errorf("file content: %w", err)
	}
	r.Path, r.Content = filepath.ToSlash(filepath.Clean(path)), content
	r.Display = fmt.Sprintf("# write %s\n%s", r.Path, content)
	if !strings.HasSuffix(r.Display, "\n") {
		r.Display += "\n"
	}
	r.Args = []string{"write", r.Path}
	return r, nil
}

func safeRel(p string) error {
	c := filepath.Clean(p)
	if p == "" || filepath.IsAbs(c) || c == "." || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return fmt.Errorf("file path %q must stay inside the output directory", p)
	}
	return nil
}

func noopDisplay(a graph.Action) string {
	var b strings.Builder
	b.WriteString("# no system call: the approval is recorded and people carry out the change\n")
	name := a.Name
	if name == "" {
		name = a.ID
	}
	b.WriteString(name + "\n")
	if a.Description != "" {
		b.WriteString(a.Description + "\n")
	}
	return b.String()
}

func (e *Executor) client() *http.Client {
	if e.HTTP != nil {
		return e.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (e *Executor) webhook(ctx context.Context, r Rendered) Result {
	res := Result{Mode: e.mode(), Kind: graph.KindWebhook, Args: []string{r.Method, r.URL}}
	if res.Mode != ModeApply {
		res.OK = true
		res.Output = fmt.Sprintf("dry-run: would send %s %s (%d byte body)", r.Method, r.URL, len(r.Body))
		return res
	}
	u, missing := envx.Expand(r.URL)
	if len(missing) > 0 {
		res.Error = "set " + strings.Join(missing, ", ") + " to send this webhook"
		return res
	}
	pu, err := url.Parse(u)
	if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
		res.Error = "webhook url must be an absolute http or https URL"
		return res
	}
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, u, body)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	for k, v := range r.Headers {
		val, missing := envx.Expand(v)
		if len(missing) > 0 {
			res.Error = fmt.Sprintf("header %s: set %s", k, strings.Join(missing, ", "))
			return res
		}
		req.Header.Set(k, val)
	}
	if r.Key != "" {
		req.Header.Set("Idempotency-Key", r.Key)
		req.Header.Set("X-Zyntra-Proposal", r.Key)
	}
	resp, err := e.client().Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		res.Error = fmt.Sprintf("%s %s: %v", r.Method, pu.Host, err)
		return res
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	sum := sha256.Sum256(b)
	res.Status, res.ResponseHash = resp.StatusCode, hex.EncodeToString(sum[:])
	res.Output = fmt.Sprintf("%s %s%s -> HTTP %d (response sha256 %s)", r.Method, pu.Host, pu.Path, resp.StatusCode, res.ResponseHash[:16])
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		res.Error = fmt.Sprintf("webhook returned HTTP %d", resp.StatusCode)
		return res
	}
	res.OK = true
	return res
}

func (e *Executor) file(r Rendered) Result {
	res := Result{Mode: e.mode(), Kind: graph.KindFile, Args: []string{"write", r.Path}}
	if err := safeRel(r.Path); err != nil {
		res.Error = err.Error()
		return res
	}
	if res.Mode != ModeApply {
		res.OK = true
		res.Output = fmt.Sprintf("dry-run: would write %s (%d bytes)", r.Path, len(r.Content))
		return res
	}
	if e.OutDir == "" {
		res.Error = "no output directory configured for file actions"
		return res
	}
	root, err := filepath.Abs(e.OutDir)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	full := filepath.Join(root, filepath.FromSlash(r.Path))
	if rel, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rel, "..") {
		res.Error = "file path escapes the output directory"
		return res
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		res.Error = err.Error()
		return res
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if _, err := f.WriteString(r.Content); err != nil {
		f.Close()
		res.Error = err.Error()
		return res
	}
	if err := f.Close(); err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK, res.Written = true, full
	res.Output = fmt.Sprintf("wrote %s (%d bytes)", full, len(r.Content))
	return res
}
