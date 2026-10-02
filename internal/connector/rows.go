// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq" // postgres driver; sqlite is registered by the ontology package

	"github.com/zyvorai/zyntra/internal/ontology"
)

// KubeQuery is what a Kubernetes connector asks kubectl for. Resource and
// Namespace are validated as plain names; the selectors are passed as the
// values of -l and --field-selector, never as separate arguments.
type KubeQuery struct {
	Resource, Namespace, Selector, FieldSelector string
}

// KubectlGet runs "kubectl get <resource> -o json" and returns its output as a
// stream; the caller closes it.
type KubectlGet func(ctx context.Context, q KubeQuery) (io.ReadCloser, error)

// maxKubeOutput bounds one listing. A cluster that needs more should be
// narrowed with a selector.
const maxKubeOutput = 512 << 20

// Kubectl returns the real runner.
func Kubectl(kubeconfig string) KubectlGet {
	return func(ctx context.Context, q KubeQuery) (io.ReadCloser, error) {
		args := []string{"get", q.Resource, "-o", "json"}
		if q.Namespace == "" {
			args = append(args, "--all-namespaces")
		} else {
			args = append(args, "-n", q.Namespace)
		}
		if q.Selector != "" {
			args = append(args, "-l", q.Selector)
		}
		if q.FieldSelector != "" {
			args = append(args, "--field-selector", q.FieldSelector)
		}
		if kubeconfig != "" {
			args = append([]string{"--kubeconfig", kubeconfig}, args...)
		}
		cmd := exec.CommandContext(ctx, "kubectl", args...)
		var stderr strings.Builder
		cmd.Stderr = &limitedWriter{w: &stderr, n: 2048}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("kubectl: %w", err)
		}
		return &cmdReader{r: io.LimitReader(out, maxKubeOutput+1), cmd: cmd, stderr: &stderr}, nil
	}
}

type limitedWriter struct {
	w *strings.Builder
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// cmdReader streams a command's stdout and reports its failure on Close.
type cmdReader struct {
	r      io.Reader
	cmd    *exec.Cmd
	stderr *strings.Builder
	read   int64
}

func (c *cmdReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += int64(n)
	if c.read > maxKubeOutput {
		return n, fmt.Errorf("kubectl output is larger than %d MiB; narrow the listing with k8s_selector, k8s_field_selector or k8s_namespace", maxKubeOutput>>20)
	}
	return n, err
}

func (c *cmdReader) Close() error {
	// Drain nothing: closing early (an error mid-stream) kills the command.
	if c.cmd.ProcessState == nil {
		_ = c.cmd.Process.Kill()
	}
	if err := c.cmd.Wait(); err != nil && c.read <= maxKubeOutput {
		if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
			return fmt.Errorf("kubectl: %w: %.200s", err, msg)
		}
		return fmt.Errorf("kubectl: %w", err)
	}
	return nil
}

// Kubernetes lists one resource kind, flattens each item to a row with Fields
// and maps the rows to objects. It is a snapshot: objects that disappear from
// the cluster are not removed here; their facts simply stop being refreshed
// and age out under an action's evidence rule.
type Kubernetes struct {
	Spec   ontology.ConnectorSpec
	Run    KubectlGet
	Schema *ontology.Schema
}

func (k Kubernetes) Name() string { return "kubernetes:" + k.Spec.Name }

func (k Kubernetes) Pull(ctx context.Context, _ time.Time) ([]ontology.Record, error) {
	rc, err := k.Run(ctx, KubeQuery{Resource: k.Spec.Resource, Namespace: k.Spec.K8sNamespace,
		Selector: k.Spec.K8sSelector, FieldSelector: k.Spec.K8sFieldSelector})
	if err != nil {
		return nil, err
	}
	rows, derr := ItemsToRows(rc, k.Spec.Fields)
	if cerr := rc.Close(); cerr != nil && derr == nil {
		derr = cerr
	}
	if derr != nil {
		return nil, fmt.Errorf("%s: %w", k.Name(), derr)
	}
	return k.Spec.Mapping.FromRows(rows, k.Schema, k.Name(), time.Now().UTC())
}

// ItemsToRows flattens a kubectl list into rows; fields maps a column to a
// dotted path into each item. A missing path leaves the column out. It reads
// the stream one item at a time, so memory stays near the size of one object
// plus the rows kept, not the whole listing.
func ItemsToRows(r io.Reader, fields map[string]string) ([]any, error) {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("decode kubectl output: expected a JSON object")
	}
	var rows []any
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("decode kubectl output: %w", err)
		}
		if key != "items" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("decode kubectl output: %w", err)
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('[') {
			return nil, fmt.Errorf("decode kubectl output: items is not a list")
		}
		for dec.More() {
			var item any
			if err := dec.Decode(&item); err != nil {
				return nil, fmt.Errorf("decode kubectl output: %w", err)
			}
			row := map[string]any{}
			for col, path := range fields {
				if v, ok := Path(item, path); ok {
					row[col] = v
				}
			}
			rows = append(rows, row)
		}
		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("decode kubectl output: %w", err)
		}
	}
	return rows, nil
}

// Path reads a dotted path from decoded JSON. A backslash escapes a dot, so
// status.capacity.nvidia\.com/gpu works; a numeric segment indexes an array,
// and [key=value] picks the first array element whose key equals value, which
// is how to read a node's Ready condition without depending on its position:
// status.conditions.[type=Ready].status
func Path(v any, path string) (any, bool) {
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
		switch x := v.(type) {
		case map[string]any:
			next, ok := x[s]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			if k, want, ok := keyedSegment(s); ok {
				found := false
				for _, el := range x {
					if m, isMap := el.(map[string]any); isMap && fmt.Sprint(m[k]) == want {
						v, found = el, true
						break
					}
				}
				if !found {
					return nil, false
				}
				continue
			}
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 || n >= len(x) {
				return nil, false
			}
			v = x[n]
		default:
			return nil, false
		}
	}
	switch v.(type) {
	case map[string]any, []any, nil:
		return nil, false // only scalars become cells
	}
	return v, true
}

// maxSQLRows bounds one pull; a larger load should be narrowed by the query.
const maxSQLRows = 200000

// SQL runs a read-only query and maps the result rows to objects. The query
// receives one argument: the time of the last successful run.
type SQL struct {
	Spec   ontology.ConnectorSpec
	Schema *ontology.Schema
	// Open opens the database; tests replace it.
	Open func(driver, dsn string) (*sql.DB, error)

	db *sql.DB
}

func (q *SQL) Name() string { return "sql:" + q.Spec.Name }

func (q *SQL) conn() (*sql.DB, error) {
	if q.db != nil {
		return q.db, nil
	}
	dsn := os.Getenv(q.Spec.DSNEnv)
	if dsn == "" {
		return nil, fmt.Errorf("%s: environment variable %s is empty", q.Name(), q.Spec.DSNEnv)
	}
	open := q.Open
	if open == nil {
		open = sql.Open
	}
	driver := q.Spec.Driver
	db, err := open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name(), redact(err, dsn))
	}
	db.SetMaxOpenConns(2)
	db.SetConnMaxLifetime(10 * time.Minute)
	q.db = db
	return db, nil
}

// redact keeps a connection string, which may hold a password, out of errors.
func redact(err error, secret string) error {
	if secret == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), secret, "[dsn]"))
}

func (q *SQL) Pull(ctx context.Context, since time.Time) ([]ontology.Record, error) {
	return q.run(ctx, q.Spec.Query, since)
}

// Keys runs ReconcileQuery, a full listing of the rows that still exist, and
// returns their records. Only the identity of each record (type, namespace,
// key) is meant to be used: it lets the scheduler find rows deleted at the
// source, which an incremental query cannot report.
func (q *SQL) Keys(ctx context.Context) ([]ontology.Record, error) {
	return q.run(ctx, q.Spec.ReconcileQuery, time.Time{})
}

func (q *SQL) run(ctx context.Context, query string, since time.Time) ([]ontology.Record, error) {
	if err := ontology.CheckReadOnlyQuery(query); err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name(), err)
	}
	db, err := q.conn()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name(), redact(err, os.Getenv(q.Spec.DSNEnv)))
	}
	defer tx.Rollback() //nolint:errcheck // read-only
	if since.IsZero() {
		since = time.Unix(0, 0)
	}
	// A query with no parameter is a full listing and takes no argument.
	var args []any
	if ontology.QueryHasParam(query) {
		args = append(args, since.UTC().Format(time.RFC3339))
	}
	rs, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", q.Name(), redact(err, os.Getenv(q.Spec.DSNEnv)))
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		return nil, err
	}
	var rows []any
	for rs.Next() {
		if len(rows) >= maxSQLRows {
			return nil, fmt.Errorf("%s: more than %d rows; narrow the query", q.Name(), maxSQLRows)
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			switch v := vals[i].(type) {
			case nil:
			case []byte:
				row[c] = string(v)
			case time.Time:
				row[c] = v.UTC().Format(time.RFC3339)
			case int64:
				row[c] = float64(v)
			default:
				row[c] = v
			}
		}
		rows = append(rows, row)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return q.Spec.Mapping.FromRows(rows, q.Schema, q.Name(), time.Now().UTC())
}

// Close releases the connection pool.
func (q *SQL) Close() error {
	if q.db != nil {
		return q.db.Close()
	}
	return nil
}

// keyedSegment parses "[key=value]".
func keyedSegment(s string) (key, value string, ok bool) {
	if len(s) < 5 || s[0] != '[' || s[len(s)-1] != ']' {
		return "", "", false
	}
	key, value, ok = strings.Cut(s[1:len(s)-1], "=")
	return key, value, ok && key != ""
}
