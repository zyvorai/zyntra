// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

// Package httpsrc is a small authenticated HTTP client for the Zyvor products
// Zyntra reads from (Netra, Gravia, Fabric).
package httpsrc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenFunc returns a bearer token, fetching or refreshing it as needed.
type TokenFunc func(ctx context.Context, c *Client) (string, error)

type Client struct {
	Name    string
	BaseURL string
	HTTP    *http.Client

	mu     sync.Mutex
	token  string
	static string
	login  TokenFunc
}

func New(name, baseURL string, insecure bool) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed lab certificates
	}
	return &Client{
		Name: name, BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{Timeout: 10 * time.Second, Transport: tr},
	}
}

// WithBearer sets a static bearer token.
func (c *Client) WithBearer(tok string) *Client { c.static = tok; return c }

// WithLogin sets a token source used (and re-used after a 401) when no static
// token is configured.
func (c *Client) WithLogin(f TokenFunc) *Client { c.login = f; return c }

func (c *Client) bearer(ctx context.Context, refresh bool) (string, error) {
	if c.static != "" {
		return c.static, nil
	}
	if c.login == nil {
		return "", nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && !refresh {
		return c.token, nil
	}
	t, err := c.login(ctx, c)
	if err != nil {
		return "", fmt.Errorf("%s login: %w", c.Name, err)
	}
	c.token = t
	return t, nil
}

// Do sends a request with auth and returns the body for 2xx responses.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string) ([]byte, error) {
	return c.do(ctx, method, path, body, contentType, true)
}

// PostJSONAnon is PostJSON without credentials, for login calls made from a
// TokenFunc.
func (c *Client) PostJSONAnon(ctx context.Context, path string, v, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, path, strings.NewReader(string(b)), "application/json", false)
	if err != nil {
		return err
	}
	if out == nil || len(resp) == 0 {
		return nil
	}
	return json.Unmarshal(resp, out)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string, withAuth bool) ([]byte, error) {
	var payload []byte
	if body != nil {
		b, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		payload = b
	}
	for attempt := 0; attempt < 2; attempt++ {
		var tok string
		if withAuth {
			var err error
			if tok, err = c.bearer(ctx, attempt > 0); err != nil {
				return nil, err
			}
		}
		var rd io.Reader
		if payload != nil {
			rd = strings.NewReader(string(payload))
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
		if err != nil {
			return nil, err
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.5")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.Name, err)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if withAuth && resp.StatusCode == http.StatusUnauthorized && c.login != nil && c.static == "" && attempt == 0 {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			msg := strings.TrimSpace(string(b))
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return nil, fmt.Errorf("%s %s %s: HTTP %d: %s", c.Name, method, path, resp.StatusCode, msg)
		}
		return b, nil
	}
	return nil, fmt.Errorf("%s %s %s: unauthorized", c.Name, method, path)
}

func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, path, nil, "")
}

// GetJSON decodes a GET response into any.
func (c *Client) GetJSON(ctx context.Context, path string) (any, error) {
	b, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s %s: decode: %w", c.Name, path, err)
	}
	return v, nil
}

// PostJSON sends v as JSON and decodes the response into out (if non-nil).
func (c *Client) PostJSON(ctx context.Context, path string, v, out any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, http.MethodPost, path, strings.NewReader(string(b)), "application/json")
	if err != nil {
		return err
	}
	if out == nil || len(resp) == 0 {
		return nil
	}
	return json.Unmarshal(resp, out)
}
