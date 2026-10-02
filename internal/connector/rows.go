// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package connector

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq" // postgres driver; sqlite is registered by the ontology package

	"github.com/zyvorai/zyntra/internal/ontology"
)

// KubectlGet runs "kubectl get <resource> -o json" and returns its output.
type KubectlGet func(ctx context.Context, resource, namespace string) ([]byte, error)

// Kubectl returns the real runner. resource and namespace have already been
// validated against a plain-name pattern, so neither can smuggle in a flag.
func Kubectl(kubeconfig string) KubectlGet {
	return func(ctx context.Context, resource, namespace string) ([]byte, error) {
		args := []string{"get", resource, "-o", "json"}
		if namespace == "" {
			args = append(args, "--all-namespaces")
		} else {
			args = append(args, "-n", namespace)
		}
		if kubeconfig != "" {
			args = append([]string{"--kubeconfig", kubeconfig}, args...)
		}
		out, err := exec.CommandContext(ctx, "kubectl", args...).Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return nil, fmt.Errorf("kubectl: %w: %.200s", err, strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, fmt.Errorf("kubectl: %w", err)
		}
		return out, nil
	}
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
	b, err := k.Run(ctx, k.Spec.Resource, k.Spec.K8sNamespace)
	if err != nil {
		return nil, err
	}
	rows, err := ItemsToRows(b, k.Spec.Fields)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", k.Name(), err)
	}
	return k.Spec.Mapping.FromRows(rows, k.Schema, k.Name(), time.Now().UTC())
}

// ItemsToRows flattens a kubectl list into rows; fields maps a column to a
// dotted path into each item. A missing path leaves the column out.
func ItemsToRows(b []byte, fields map[string]string) ([]any, error) {
	var list struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("decode kubectl output: %w", err)
	}
	rows := make([]any, 0, len(list.Items))
	for _, it := range list.Items {
		row := map[string]any{}
		for col, path := range fields {
			if v, ok := Path(it, path); ok {
				row[col] = v
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Path reads a dotted path from decoded JSON. A backslash escapes a dot, so
// status.capacity.nvidia\.com/gpu works; a numeric segment indexes an array.
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
	if err := ontology.CheckReadOnlyQuery(q.Spec.Query); err != nil {
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
	rs, err := tx.QueryContext(ctx, q.Spec.Query, since.UTC().Format(time.RFC3339))
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
