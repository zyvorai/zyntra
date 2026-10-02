// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Package connector is the SDK for bringing business records into the
// ontology. A connector pulls typed records; the ontology store validates
// them, records provenance and audits the batch. Connectors never touch the
// KPI graph.
package connector

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/zyvorai/zyntra/internal/ontology"
)

// Connector pulls records. since is the time of the last successful pull
// (zero on the first), for sources that can send only changes.
type Connector interface {
	Name() string
	Pull(ctx context.Context, since time.Time) ([]ontology.Record, error)
}

// File reads one pack mapping.
type File struct {
	Mapping ontology.Mapping
	Dir     string
	Schema  *ontology.Schema
	Load    ontology.Loader
}

func (f File) Name() string { return "file:" + f.Mapping.Source }

func (f File) Pull(_ context.Context, _ time.Time) ([]ontology.Record, error) {
	return f.Mapping.Records(f.Dir, f.Schema, f.Load)
}

// maxBody bounds what a connector may return.
const maxBody = 32 << 20

// Exec runs a program that writes one JSON record per line to stdout. The
// last pull time is passed in ZYNTRA_SINCE (RFC 3339). It runs only with the
// operator's explicit opt-in, since it executes a command from the pack.
type Exec struct {
	Spec    string
	Command []string
	Timeout time.Duration
}

// ExecEnabled reports whether the operator allowed exec connectors.
func ExecEnabled() bool { return os.Getenv("ZYNTRA_CONNECTOR_EXEC") == "1" }

func (e Exec) Name() string { return "exec:" + e.Spec }

func (e Exec) Pull(ctx context.Context, since time.Time) ([]ontology.Record, error) {
	if !ExecEnabled() {
		return nil, errors.New("exec connectors are disabled; set ZYNTRA_CONNECTOR_EXEC=1 to allow them")
	}
	if len(e.Command) == 0 {
		return nil, errors.New("exec connector has no command")
	}
	to := e.Timeout
	if to <= 0 {
		to = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Command[0], e.Command[1:]...)
	cmd.Env = append(os.Environ(), "ZYNTRA_SINCE="+since.UTC().Format(time.RFC3339))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	recs, perr := ReadLines(io.LimitReader(out, maxBody))
	if werr := cmd.Wait(); werr != nil {
		return nil, fmt.Errorf("%s: %w: %s", e.Name(), werr, bytes.TrimSpace(stderr.Bytes()))
	}
	return recs, perr
}

// ReadLines parses JSON-lines records.
func ReadLines(r io.Reader) ([]ontology.Record, error) {
	var out []ontology.Record
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec ontology.Record
		if err := json.Unmarshal(line, &rec); err != nil {
			return out, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// HTTP GETs a JSON array of records. ?since=<RFC 3339> is appended.
type HTTP struct {
	Spec   string
	URL    string
	Token  string
	Client *http.Client
}

func (h HTTP) Name() string { return "http:" + h.Spec }

func (h HTTP) Pull(ctx context.Context, since time.Time) ([]ontology.Record, error) {
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%s: bad url", h.Name())
	}
	if !since.IsZero() {
		q := u.Query()
		q.Set("since", since.UTC().Format(time.RFC3339))
		u.RawQuery = q.Encode()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	c := h.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", h.Name(), resp.Status)
	}
	var recs []ontology.Record
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&recs); err != nil {
		return nil, fmt.Errorf("%s: %w", h.Name(), err)
	}
	return recs, nil
}

// Options carry what connectors need from the host.
type Options struct {
	Kubeconfig string
	// Kubectl replaces the real runner; tests use it.
	Kubectl KubectlGet
	// OpenSQL replaces sql.Open; tests use it.
	OpenSQL func(driver, dsn string) (*sql.DB, error)
}

// FromSpec builds the connector a pack declares.
func FromSpec(c ontology.ConnectorSpec, schema *ontology.Schema, o Options) (Connector, error) {
	switch c.Kind {
	case "exec":
		return Exec{Spec: c.Name, Command: c.Command}, nil
	case "http":
		return HTTP{Spec: c.Name, URL: c.URL, Token: os.Getenv(c.TokenEnv)}, nil
	case "kubernetes":
		run := o.Kubectl
		switch {
		case run != nil:
		case o.Kubeconfig == "" && InCluster():
			// In a pod with no kubeconfig given: read this cluster through
			// the service account, which needs no kubectl binary.
			run = KubeAPI()
		default:
			run = Kubectl(o.Kubeconfig)
		}
		return Kubernetes{Spec: c, Run: run, Schema: schema}, nil
	case "sql":
		return &SQL{Spec: c, Schema: schema, Open: o.OpenSQL}, nil
	case "rest":
		return REST{Spec: c, Schema: schema}, nil
	}
	return nil, fmt.Errorf("connector %s: unknown kind %q", c.Name, c.Kind)
}

// Run pulls from c and ingests the batch.
func Run(ctx context.Context, st *ontology.Store, c Connector, since time.Time, by string, now time.Time) (ontology.IngestReport, error) {
	return run(ctx, st, c, since, by, now, false)
}

// RunSnapshot is Run for a connector that lists everything it knows: objects
// it created that are no longer listed are removed.
func RunSnapshot(ctx context.Context, st *ontology.Store, c Connector, since time.Time, by string, now time.Time) (ontology.IngestReport, error) {
	return run(ctx, st, c, since, by, now, true)
}

func run(ctx context.Context, st *ontology.Store, c Connector, since time.Time, by string, now time.Time, prune bool) (ontology.IngestReport, error) {
	recs, err := c.Pull(ctx, since)
	if err != nil {
		return ontology.IngestReport{Source: c.Name()}, err
	}
	if prune {
		return st.IngestSnapshot(c.Name(), by, recs, now)
	}
	return st.Ingest(c.Name(), by, recs, now)
}
