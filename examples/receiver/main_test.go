// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReceiver(t *testing.T) {
	dir := t.TempDir()
	b := &inbox{dir: dir, seen: map[string]string{}}
	if err := b.load(); err != nil {
		t.Fatal(err)
	}
	post := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/pos/markdowns", strings.NewReader(`{"percent":10}`))
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	if w := post("prop-1"); w.Code != http.StatusCreated {
		t.Fatalf("first delivery: %d %s", w.Code, w.Body)
	}
	if w := post("prop-1"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("repeat delivery: %d %s", w.Code, w.Body)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("stored %d files, want 1", len(files))
	}
	raw, _ := os.ReadFile(files[0])
	if strings.Contains(string(raw), "secret") {
		t.Fatal("Authorization must not be stored")
	}
	var d Delivery
	if err := json.Unmarshal(raw, &d); err != nil || d.Key != "prop-1" || d.Path != "/pos/markdowns" {
		t.Fatalf("stored delivery %+v (%v)", d, err)
	}

	again := &inbox{dir: dir, seen: map[string]string{}}
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	if _, ok := again.seen["prop-1"]; !ok {
		t.Fatal("a restart must remember idempotency keys")
	}
}
