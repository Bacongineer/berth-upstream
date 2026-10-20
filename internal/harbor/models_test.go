// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package harbor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/models"
)

// tinyGGUF writes the smallest file models.ReadGGUF decodes: two metadata
// pairs and no tensors, which is all a listing row needs.
func tinyGGUF() []byte {
	var b bytes.Buffer
	put := func(v any) {
		if err := binary.Write(&b, binary.LittleEndian, v); err != nil {
			panic(err) // never happens for a bytes.Buffer
		}
	}
	putStr := func(s string) {
		put(uint64(len(s)))
		b.WriteString(s)
	}
	b.WriteString("GGUF")
	put(uint32(3)) // version
	put(uint64(0)) // tensor count
	put(uint64(2)) // metadata pairs
	putStr("general.architecture")
	put(uint32(8)) // typeString
	putStr("llama")
	putStr("general.file_type")
	put(uint32(4))  // typeUint32
	put(uint32(15)) // Q4_K_M
	return b.Bytes()
}

func writeModel(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestModelsEndpoint(t *testing.T) {
	root := t.TempDir()
	writeModel(t, root, "synth-8b.gguf", tinyGGUF())
	// a companion file the listing counts but does not list: the projector
	// next to its one model, which is how a library keeps them
	writeModel(t, root, "synth-8b-mmproj.gguf", tinyGGUF())
	// an unreadable model in its own root: it fails the listing, not the scan
	broken := t.TempDir()
	writeModel(t, broken, "broken.gguf", []byte("not a gguf at all"))

	h := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"), Dirs: []string{root, broken},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("/v1/models: %s %s", resp.Status, b)
	}
	var doc libraryDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("payload does not decode: %v", err)
	}
	if doc.CacheChecked != true {
		t.Fatal("cache_checked must always name whether the CACHED column was measured")
	}
	if doc.Files != 3 || doc.Companions != 1 || doc.Count != 2 {
		t.Fatalf("files=%d companions=%d count=%d, want 3/1/2", doc.Files, doc.Companions, doc.Count)
	}
	if doc.Status != "failed" || len(doc.Errors) != 1 || doc.Errors[0].Name != "broken" {
		t.Fatalf("errors: %+v, want broken.gguf's read failure", doc.Errors)
	}
	if len(doc.Models) != doc.Count {
		t.Fatalf("count %d does not match %d rows", doc.Count, len(doc.Models))
	}
	var m models.Model
	for _, row := range doc.Models {
		if row.Name == "synth-8b" {
			m = row
		}
	}
	if m.Name == "" {
		t.Fatalf("rows: %+v", doc.Models)
	}
	if m.Name != "synth-8b" || m.Shelf != "chat" || m.Quant != "Q4_K_M" {
		t.Fatalf("row: %+v", m)
	}
	// the CACHED figures ride on the model itself; the share is the kernel
	// page cache's own reading, so the test pins the shape, never the number
	if m.CachedSize <= 0 || m.CachedBytes > m.CachedSize {
		t.Fatalf("cached figures: %d of %d measured", m.CachedBytes, m.CachedSize)
	}
}

// TestModelsCache proves the snapshot is the work the cache holds: a second
// ask inside the ttl answers without another walk, even once the root has
// grown, and an expired snapshot walks again.
func TestModelsCache(t *testing.T) {
	root := t.TempDir()
	writeModel(t, root, "a.gguf", tinyGGUF())
	h := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"), Dirs: []string{root},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	get := func() (int, libraryDoc) {
		resp, err := http.Get(srv.URL + "/v1/models")
		if err != nil {
			t.Fatal(err)
		}
		var doc libraryDoc
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("payload does not decode: %v", err)
		}
		return resp.StatusCode, doc
	}
	_, first := get()
	if first.Count != 1 {
		t.Fatalf("first walk: %d models", first.Count)
	}
	writeModel(t, root, "b.gguf", tinyGGUF())
	_, second := get()
	if second.Count != 1 {
		t.Fatalf("the ttl must serve the snapshot, not walk a changing root: %d models", second.Count)
	}
	// expire the snapshot the way time would
	h.library.mu.Lock()
	h.library.at = time.Now().Add(-modelsTTL - time.Second)
	h.library.mu.Unlock()
	_, third := get()
	if third.Count != 2 {
		t.Fatalf("after the ttl: %d models, want a fresh walk", third.Count)
	}
}

// TestModelsNoDirs: a harbor with no roots of its own finds none of this
// machine's, reports the scan's own failure row instead of empty success.
func TestModelsNoDirs(t *testing.T) {
	h := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	h.Dirs = []string{filepath.Join(t.TempDir(), "missing-root")}
	resp, err := http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var doc libraryDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Count != 1 || doc.Models[0].Err == "" {
		t.Fatalf("missing root: %d rows, first %+v; the scan's failure row must say so", doc.Count, doc.Models)
	}
}
