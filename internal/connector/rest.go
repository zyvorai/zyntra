// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

// Bounds on one pull, so a runaway or hostile API cannot exhaust memory or
// keep the connector paging forever.
const (
	maxRESTPages = 1000
	maxRESTItems = 500000
	maxRESTBody  = 64 << 20
)

// REST reads a paged JSON API, flattens each item with Fields and maps the
// rows to objects. The bearer token (or basic-auth login) is sent only to the
// host of the configured URL: a next-page link or a redirect that points
// anywhere else is refused, so a compromised API cannot collect the token.
type REST struct {
	Spec   ontology.ConnectorSpec
	Schema *ontology.Schema
	// Client replaces the default (tests).
	Client *http.Client
}

func (r REST) Name() string { return "rest:" + r.Spec.Name }

func (r REST) client(host string) *http.Client {
	if r.Client != nil {
		return r.Client
	}
	return &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Host != host {
				return fmt.Errorf("refusing a redirect to another host (%s)", req.URL.Host)
			}
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

func (r REST) auth(req *http.Request) {
	switch {
	case r.Spec.TokenEnv != "":
		if tok := os.Getenv(r.Spec.TokenEnv); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	case r.Spec.UserEnv != "":
		req.SetBasicAuth(os.Getenv(r.Spec.UserEnv), os.Getenv(r.Spec.PassEnv))
	}
}

// secrets lists the values to keep out of error text.
func (r REST) secrets() []string {
	var out []string
	for _, env := range []string{r.Spec.TokenEnv, r.Spec.PassEnv, r.Spec.UserEnv} {
		if env != "" && os.Getenv(env) != "" {
			out = append(out, os.Getenv(env))
		}
	}
	return out
}

func (r REST) scrub(err error) error {
	msg := err.Error()
	for _, s := range r.secrets() {
		msg = strings.ReplaceAll(msg, s, "[secret]")
	}
	// A URL can carry credentials or a token in its query.
	var ue *url.Error
	if errors.As(err, &ue) {
		msg = strings.Replace(msg, ue.URL, redactURL(ue.URL), 1)
	}
	return errors.New(msg)
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[url]"
	}
	u.User, u.RawQuery = nil, ""
	return u.String()
}

func (r REST) Pull(ctx context.Context, since time.Time) ([]ontology.Record, error) {
	base, err := url.Parse(r.Spec.URL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("%s: bad url", r.Name())
	}
	if r.Spec.SinceParam != "" && !since.IsZero() {
		q := base.Query()
		q.Set(r.Spec.SinceParam, since.UTC().Format(time.RFC3339))
		base.RawQuery = q.Encode()
	}
	cli := r.client(base.Host)
	next := base.String()
	var rows []any
	for page := 1; next != ""; page++ {
		if page > maxRESTPages {
			return nil, fmt.Errorf("%s: more than %d pages; narrow the request", r.Name(), maxRESTPages)
		}
		u := next
		if r.Spec.PageParam != "" {
			pu, _ := url.Parse(next)
			q := pu.Query()
			q.Set(r.Spec.PageParam, strconv.Itoa(page))
			if r.Spec.PageSize > 0 {
				name := r.Spec.PageSizeParam
				if name == "" {
					name = "limit"
				}
				q.Set(name, strconv.Itoa(r.Spec.PageSize))
			}
			pu.RawQuery = q.Encode()
			u = pu.String()
		}
		body, err := r.get(ctx, cli, u, base.Host)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r.Name(), r.scrub(err))
		}
		var doc any
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("%s: page %d is not JSON: %w", r.Name(), page, err)
		}
		items := doc
		if r.Spec.Items != "" {
			v, ok := pathAny(doc, r.Spec.Items)
			if !ok {
				return nil, fmt.Errorf("%s: no %q in the response", r.Name(), r.Spec.Items)
			}
			items = v
		}
		list, ok := items.([]any)
		if !ok {
			return nil, fmt.Errorf("%s: the items are not a list", r.Name())
		}
		for _, it := range list {
			row := map[string]any{}
			for col, p := range r.Spec.Fields {
				if v, ok := Path(it, p); ok {
					row[col] = v
				}
			}
			rows = append(rows, row)
		}
		if len(rows) > maxRESTItems {
			return nil, fmt.Errorf("%s: more than %d items; narrow the request", r.Name(), maxRESTItems)
		}
		switch {
		case r.Spec.PageParam != "":
			// A short or empty page is the last one.
			size := r.Spec.PageSize
			if len(list) == 0 || (size > 0 && len(list) < size) {
				next = ""
			}
		case r.Spec.Next != "":
			n, _ := pathAny(doc, r.Spec.Next)
			ns, _ := n.(string)
			if ns == "" {
				next = ""
				break
			}
			nu, err := base.Parse(ns) // a relative link resolves against the request
			if err != nil || nu.Host != base.Host || nu.Scheme != base.Scheme {
				return nil, fmt.Errorf("%s: the next-page link leaves %s; refusing to send credentials there", r.Name(), base.Host)
			}
			next = nu.String()
		default:
			next = ""
		}
	}
	return r.Spec.Mapping.FromRows(rows, r.Schema, r.Name(), time.Now().UTC())
}

func (r REST) get(ctx context.Context, cli *http.Client, u, host string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	r.auth(req)
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRESTBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRESTBody {
		return nil, fmt.Errorf("a page is larger than %d MiB", maxRESTBody>>20)
	}
	return b, nil
}

// pathAny is Path for any JSON value, including lists and objects.
func pathAny(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	var segs []string
	var cur strings.Builder
	for i := 0; i < len(path); i++ {
		switch {
		case path[i] == '\\' && i+1 < len(path):
			i++
			cur.WriteByte(path[i])
		case path[i] == '.':
			segs = append(segs, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(path[i])
		}
	}
	segs = append(segs, cur.String())
	for _, s := range segs {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[s]; !ok {
			return nil, false
		}
	}
	return v, true
}
