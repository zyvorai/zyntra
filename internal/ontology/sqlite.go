// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ontology

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver: CGO_ENABLED=0 builds keep working
)

// sqliteBackend stores each object, link, candidate and change as a row, so a
// write touches only what changed and the change log can be queried without
// loading it. Objects and links are still held in memory by the Store; this
// removes the write amplification and the file-size limit, not the memory
// footprint.
type sqliteBackend struct{ db *sql.DB }

const schemaSQL = `
CREATE TABLE IF NOT EXISTS objects (id TEXT PRIMARY KEY, type TEXT NOT NULL, tenant TEXT NOT NULL, body BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS objects_type ON objects(type, tenant);
CREATE TABLE IF NOT EXISTS links (id TEXT PRIMARY KEY, type TEXT NOT NULL, from_id TEXT NOT NULL, to_id TEXT NOT NULL, body BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS links_from ON links(from_id);
CREATE INDEX IF NOT EXISTS links_to ON links(to_id);
CREATE TABLE IF NOT EXISTS candidates (id TEXT PRIMARY KEY, body BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS redirects (from_id TEXT PRIMARY KEY, to_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS history (seq INTEGER PRIMARY KEY AUTOINCREMENT, object TEXT NOT NULL, property TEXT NOT NULL, at TEXT NOT NULL, body BLOB NOT NULL);
CREATE INDEX IF NOT EXISTS history_object ON history(object, seq);
PRAGMA user_version = 1;
`

func openSQLite(path string) (*sqliteBackend, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	// WAL keeps readers and the single writer from blocking each other; the
	// busy timeout rides out a concurrent backup.
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("ontology sqlite: %w", err)
	}
	return &sqliteBackend{db: db}, nil
}

func (b *sqliteBackend) Close() error { return b.db.Close() }

func (b *sqliteBackend) Load() (state, error) {
	st := state{Version: 1, Objects: map[string]Object{}, Links: map[string]Link{}, Candidates: map[string]Candidate{}, Redirects: map[string]string{}}
	load := func(q string, each func(*sql.Rows) error) error {
		rows, err := b.db.Query(q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := each(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	if err := load("SELECT body FROM objects", func(r *sql.Rows) error {
		var raw []byte
		var o Object
		if err := r.Scan(&raw); err != nil || json.Unmarshal(raw, &o) != nil {
			return fmt.Errorf("bad object row: %v", err)
		}
		st.Objects[o.ID] = o
		return nil
	}); err != nil {
		return st, err
	}
	if err := load("SELECT body FROM links", func(r *sql.Rows) error {
		var raw []byte
		var l Link
		if err := r.Scan(&raw); err != nil || json.Unmarshal(raw, &l) != nil {
			return fmt.Errorf("bad link row: %v", err)
		}
		st.Links[l.ID] = l
		return nil
	}); err != nil {
		return st, err
	}
	if err := load("SELECT body FROM candidates", func(r *sql.Rows) error {
		var raw []byte
		var c Candidate
		if err := r.Scan(&raw); err != nil || json.Unmarshal(raw, &c) != nil {
			return fmt.Errorf("bad candidate row: %v", err)
		}
		st.Candidates[c.ID] = c
		return nil
	}); err != nil {
		return st, err
	}
	err := load("SELECT from_id, to_id FROM redirects", func(r *sql.Rows) error {
		var f, t string
		if err := r.Scan(&f, &t); err != nil {
			return err
		}
		st.Redirects[f] = t
		return nil
	})
	return st, err
}

func (b *sqliteBackend) Apply(ops []op, _ func() state) error {
	tx, err := b.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for _, o := range ops {
		var err error
		switch o.kind {
		case opPutObject:
			var raw []byte
			if raw, err = json.Marshal(o.obj); err == nil {
				_, err = tx.Exec("INSERT INTO objects(id,type,tenant,body) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET type=excluded.type, tenant=excluded.tenant, body=excluded.body", o.id, o.obj.Type, o.obj.Tenant, raw)
			}
		case opDelObject:
			_, err = tx.Exec("DELETE FROM objects WHERE id=?", o.id)
		case opPutLink:
			var raw []byte
			if raw, err = json.Marshal(o.link); err == nil {
				_, err = tx.Exec("INSERT INTO links(id,type,from_id,to_id,body) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", o.id, o.link.Type, o.link.From, o.link.To, raw)
			}
		case opDelLink:
			_, err = tx.Exec("DELETE FROM links WHERE id=?", o.id)
		case opPutCand:
			var raw []byte
			if raw, err = json.Marshal(o.cand); err == nil {
				_, err = tx.Exec("INSERT INTO candidates(id,body) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", o.id, raw)
			}
		case opPutRedirect:
			_, err = tx.Exec("INSERT INTO redirects(from_id,to_id) VALUES(?,?) ON CONFLICT(from_id) DO UPDATE SET to_id=excluded.to_id", o.id, o.to)
		case opAddChange:
			var raw []byte
			if raw, err = json.Marshal(o.chg); err == nil {
				_, err = tx.Exec("INSERT INTO history(object,property,at,body) VALUES(?,?,?,?)", o.chg.Object, o.chg.Property, o.chg.After.Prov.IngestedAt.UTC().Format("2006-01-02T15:04:05.000Z"), raw)
			}
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// History reads one object's change log, oldest first, straight from disk.
func (b *sqliteBackend) History(object string) ([]Change, error) {
	rows, err := b.db.Query("SELECT body FROM history WHERE object=? ORDER BY seq", object)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Change
	for rows.Next() {
		var raw []byte
		var c Change
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
