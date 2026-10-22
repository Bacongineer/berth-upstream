// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCatalogRoundTrip(t *testing.T) {
	root := t.TempDir()
	ms := []Model{
		{Name: "qwen3-30b", Path: filepath.Join(root, "qwen3-30b.Q4_K_M.gguf"), Shelf: ShelfText, Bytes: 17 << 30},
		{Name: "flux-lora", Path: filepath.Join(root, "lora", "flux.safetensors"), Shelf: ShelfImage, Bytes: 1 << 30},
		{Name: "mystery", Path: filepath.Join(root, "mystery.gguf"), Bytes: 5},
	}
	if err := WriteCatalog(root, ms); err != nil {
		t.Fatalf("WriteCatalog: %v", err)
	}
	c, err := ReadCatalog(root)
	if err != nil {
		t.Fatalf("ReadCatalog: %v", err)
	}
	if c.Root != root || c.Host == "" || c.At.IsZero() {
		t.Errorf("catalog = %+v, want root, host and a stamp", c)
	}
	if len(c.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(c.Models))
	}
	if c.Models[0].Shelf != ShelfText || c.Models[1].Shelf != ShelfImage || c.Models[2].Shelf != "" {
		t.Errorf("shelves = %v %v %v, want chat, image, none", c.Models[0].Shelf, c.Models[1].Shelf, c.Models[2].Shelf)
	}
	// A re-scan rewrites the snapshot rather than appending to it.
	if err := WriteCatalog(root, ms[:1]); err != nil {
		t.Fatal(err)
	}
	c, _ = ReadCatalog(root)
	if len(c.Models) != 1 {
		t.Errorf("after re-write = %d models, want the latest scan only", len(c.Models))
	}
}

func TestCatalogAge(t *testing.T) {
	root := t.TempDir()
	if _, err := CatalogAge(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no catalog = %v, want no-such-file", err)
	}
	if err := WriteCatalog(root, nil); err != nil {
		t.Fatal(err)
	}
	age, err := CatalogAge(root)
	if err != nil || age < 0 || age > time.Minute {
		t.Errorf("age = %v, %v; want a just-written snapshot", age, err)
	}
}

// A snapshot from the other boot of a shared root reads fine here.
func TestReadForeignCatalog(t *testing.T) {
	root := t.TempDir()
	// encoding/json builds the document, so a Windows root's backslashes
	// come out escaped the way the reader expects.
	d := struct {
		Root   string `json:"root"`
		Host   string `json:"host"`
		At     string `json:"at"`
		Models []struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			Shelf string `json:"shelf"`
			Bytes int    `json:"bytes"`
		} `json:"models"`
	}{Root: root, Host: "galaxy", At: "2026-10-19T08:00:00Z",
		Models: []struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			Shelf string `json:"shelf"`
			Bytes int    `json:"bytes"`
		}{{Name: "a", Path: root + "/a.gguf", Shelf: "chat", Bytes: 3}}}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(CatalogPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CatalogPath(root), b, 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := ReadCatalog(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "galaxy" || len(c.Models) != 1 || c.Models[0].Shelf != ShelfText {
		t.Errorf("foreign catalog = %+v", c)
	}
	if err := os.WriteFile(CatalogPath(root), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCatalog(root); err == nil {
		t.Error("a corrupt snapshot must be an error, not an empty one")
	}
}
