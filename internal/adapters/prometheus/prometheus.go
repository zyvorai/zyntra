// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package prometheus refreshes KPI values from Prometheus instant queries.
package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type response struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string          `json:"resultType"`
		Result     json.RawMessage `json:"result"`
	} `json:"data"`
}

// Query runs an instant query and returns the first sample value.
func (c *Client) Query(ctx context.Context, q string) (float64, error) {
	u := c.BaseURL + "/api/v1/query?query=" + url.QueryEscape(q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, fmt.Errorf("prometheus: decode: %w", err)
	}
	if r.Status != "success" {
		return 0, fmt.Errorf("prometheus: %s", r.Error)
	}
	var sample []any
	switch r.Data.ResultType {
	case "scalar":
		if err := json.Unmarshal(r.Data.Result, &sample); err != nil {
			return 0, err
		}
	case "vector":
		var vec []struct {
			Value []any `json:"value"`
		}
		if err := json.Unmarshal(r.Data.Result, &vec); err != nil {
			return 0, err
		}
		if len(vec) == 0 {
			return 0, fmt.Errorf("prometheus: query %q returned no samples", q)
		}
		sample = vec[0].Value
	default:
		return 0, fmt.Errorf("prometheus: unsupported result type %q", r.Data.ResultType)
	}
	if len(sample) != 2 {
		return 0, fmt.Errorf("prometheus: malformed sample")
	}
	s, ok := sample[1].(string)
	if !ok {
		return 0, fmt.Errorf("prometheus: malformed sample value")
	}
	return strconv.ParseFloat(s, 64)
}
