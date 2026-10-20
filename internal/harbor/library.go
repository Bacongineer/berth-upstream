// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package harbor

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/T-Crypt/berth/internal/models"
)

// modelsTTL is how long one scan of the model library serves /v1/models.
// The scan stats every file in every root and mincore reads their pages, so
// the page polls a snapshot instead of a fresh walk.
const modelsTTL = 30 * time.Second

// libraryDoc is what `berth models --format json` computes, over the same
// roots and the same listing rules: main models and embeddings, companions
// counted. Model marshals in full by its own tags.
type libraryDoc struct {
	Status       string         `json:"status"` // ok | failed
	Roots        []string       `json:"roots"`
	Count        int            `json:"count"`
	Bytes        uint64         `json:"bytes"` // every file in every root
	Files        int            `json:"files"`
	Companions   int            `json:"companions"` // mmproj/draft files, counted not listed
	CacheChecked bool           `json:"cache_checked"`
	Models       []models.Model `json:"models"`
	Errors       []libraryErr   `json:"errors,omitempty"`
}

type libraryErr struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Err  string `json:"err"`
}

// libraryCache holds the last snapshot and its age. Zero value ready.
type libraryCache struct {
	mu  sync.Mutex
	at  time.Time
	doc *libraryDoc
}

// libraryHandler serves GET /v1/models: the model library the node's
// machine holds, with the page-cache figure behind the CACHED column,
// recomputed at most once per modelsTTL.
func (h *Harbor) libraryHandler(w http.ResponseWriter, _ *http.Request) {
	doc := h.librarySnapshot()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(doc)
}

func (h *Harbor) librarySnapshot() *libraryDoc {
	h.library.mu.Lock()
	defer h.library.mu.Unlock()
	if h.library.doc != nil && time.Since(h.library.at) < modelsTTL {
		return h.library.doc
	}
	h.library.doc = h.scanLibrary()
	h.library.at = time.Now()
	return h.library.doc
}

// scanLibrary is one walk of the roots, the listing `berth models` prints,
// without its file writes: it neither writes catalogs back into the roots
// nor opens the harbor database, both of which the listing does.
func (h *Harbor) scanLibrary() *libraryDoc {
	dirs := h.Dirs
	if len(dirs) == 0 {
		dirs = models.DefaultRoots()
	}
	doc := &libraryDoc{Status: "ok", Roots: dirs}
	ms := models.Scan(dirs)
	models.ApplyRootShelves(ms, h.Shelves)
	for _, m := range ms {
		doc.Bytes += m.Bytes
		if len(m.Files) > 0 {
			doc.Files += len(m.Files)
		} else {
			doc.Files++
		}
		if m.Err != "" {
			doc.Status = "failed"
			doc.Errors = append(doc.Errors, libraryErr{Name: m.Name, Path: m.Path, Err: m.Err})
		}
	}
	listed := models.Main(ms)
	seen := map[string]bool{}
	for _, m := range ms {
		for _, c := range m.Companions {
			seen[c] = true
		}
	}
	doc.Companions = len(seen)
	models.Cache(listed)
	doc.CacheChecked = true
	doc.Models = listed
	doc.Count = len(listed)
	return doc
}
