// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

// Command receiver is a test inbox for Zyntra webhook actions. Point a
// pack's ${ZYNTRA_*_URL} at it during a pilot to see exactly what an
// approved action sends before wiring the real system:
//
//	go run ./examples/receiver -addr :9099 -dir ./inbox
//	ZYNTRA_POS_URL=http://127.0.0.1:9099/pos ZYNTRA_EXECUTE=apply zyntra serve -f packs/shop
//
// Every request is stored as JSON in -dir and listed at GET /. A repeated
// Idempotency-Key is acknowledged without storing it again, as a real
// receiver should.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxBody = 1 << 20

// Delivery is one stored request.
type Delivery struct {
	ID         string            `json:"id"`
	At         time.Time         `json:"at"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Key        string            `json:"idempotency_key,omitempty"`
	Proposal   string            `json:"proposal,omitempty"`
	Headers    map[string]string `json:"headers"`
	Body       any               `json:"body"`
	BodySHA256 string            `json:"body_sha256"`
}

type inbox struct {
	dir  string
	mu   sync.Mutex
	seen map[string]string
	list []Delivery
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// redacted headers are never stored: a test inbox must not collect secrets.
var redacted = map[string]bool{"Authorization": true, "Cookie": true, "X-Api-Key": true}

func (b *inbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/" {
		b.mu.Lock()
		out := append([]Delivery(nil), b.list...)
		b.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "deliveries": out})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "body too large or unreadable"})
		return
	}
	key := r.Header.Get("Idempotency-Key")
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.seen[key]; ok && key != "" {
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "duplicate": true})
		return
	}
	sum := sha256.Sum256(raw)
	d := Delivery{
		ID: fmt.Sprintf("%s-%03d", time.Now().UTC().Format("20060102T150405"), len(b.list)+1),
		At: time.Now().UTC(), Method: r.Method, Path: r.URL.Path, Key: key,
		Proposal: r.Header.Get("X-Zyntra-Proposal"), Headers: map[string]string{},
		BodySHA256: hex.EncodeToString(sum[:]),
	}
	for k := range r.Header {
		if redacted[k] {
			d.Headers[k] = "[redacted]"
			continue
		}
		d.Headers[k] = r.Header.Get(k)
	}
	if len(raw) > 0 && json.Unmarshal(raw, &d.Body) != nil {
		d.Body = string(raw)
	}
	if b.dir != "" {
		name := d.ID
		if key != "" {
			name += "-" + unsafeChars.ReplaceAllString(key, "_")
		}
		f, _ := json.MarshalIndent(d, "", "  ")
		if err := os.WriteFile(filepath.Join(b.dir, name+".json"), f, 0o600); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot store delivery"})
			return
		}
	}
	if key != "" {
		b.seen[key] = d.ID
	}
	b.list = append(b.list, d)
	log.Printf("%s %s key=%s proposal=%s %d bytes", d.Method, d.Path, key, d.Proposal, len(raw))
	writeJSON(w, http.StatusCreated, map[string]any{"id": d.ID, "received": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// load restores earlier deliveries so a restart still de-duplicates.
func (b *inbox) load() error {
	if b.dir == "" {
		return nil
	}
	if err := os.MkdirAll(b.dir, 0o750); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(b.dir, "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var d Delivery
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		b.list = append(b.list, d)
		if d.Key != "" {
			b.seen[d.Key] = d.ID
		}
	}
	return nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "listen address")
	dir := flag.String("dir", "", "directory to store deliveries (empty keeps them in memory)")
	flag.Parse()
	b := &inbox{dir: strings.TrimSpace(*dir), seen: map[string]string{}}
	if err := b.load(); err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *addr, Handler: b, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("zyntra webhook receiver on %s (store: %s)", *addr, map[bool]string{true: *dir, false: "memory"}[*dir != ""])
	log.Fatal(srv.ListenAndServe())
}
