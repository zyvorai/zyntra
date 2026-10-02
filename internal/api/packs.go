// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package api

import (
	"net/http"
)

// PackHeader and PackParam select which pack a request is for when one
// instance serves several. The query parameter exists because the browser's
// EventSource cannot set headers.
const (
	PackHeader = "X-Zyntra-Pack"
	PackParam  = "pack"
)

// PackInfo describes one served pack for the console's switcher.
type PackInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Industry string `json:"industry,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// handlePacks lists the packs this instance serves. It is closed to tenant
// accounts like every route that is not on their allowlist.
func (s *Server) handlePacks(w http.ResponseWriter, _ *http.Request) {
	out := s.opt.Packs
	if out == nil {
		out = []PackInfo{}
	}
	writeJSON(w, http.StatusOK, out)
}

// PackMux serves several packs from one listener. Each pack is a complete
// Server with its own model, ontology, approvals and audit chain; the sign-in
// is shared. A request names its pack with the X-Zyntra-Pack header or the
// pack query parameter, and falls back to def. A tenant account that names
// another pack still meets that pack's own tenant checks, so it sees only its
// own objects there; it can, however, tell whether a pack id exists.
func PackMux(servers map[string]*Server, def string) http.Handler {
	handlers := make(map[string]http.Handler, len(servers))
	for id, s := range servers {
		handlers[id] = s.Handler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(PackHeader)
		if id == "" {
			id = r.URL.Query().Get(PackParam)
		}
		if id == "" {
			id = def
		}
		h, ok := handlers[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown pack"})
			return
		}
		h.ServeHTTP(w, r)
	})
}
