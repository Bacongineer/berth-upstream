// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/T-Crypt/berth/internal/fsx"
)

// CatalogDir is the directory a root keeps its catalog in, beside the
// models it describes.
const CatalogDir = ".berth"

// CatalogName is the catalog file inside it.
const CatalogName = "catalog.json"

// CatalogModel is one row of a root's snapshot.
type CatalogModel struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Shelf Shelf  `json:"shelf,omitempty"`
	Bytes uint64 `json:"bytes"`
}

// Catalog is the last scan one berth left in a root. On a root shared
// between boots the sibling machine reads it to see what this side
// resolved. It is a snapshot, not a cache: the scan recomputes from the
// files and never depends on the catalog's contents, so a stale one is
// harmless.
type Catalog struct {
	Root   string         `json:"root"`
	Host   string         `json:"host"`
	At     time.Time      `json:"at"`
	Models []CatalogModel `json:"models"`
}

// CatalogPath is where root's catalog lies.
func CatalogPath(root string) string {
	return filepath.Join(root, CatalogDir, CatalogName)
}

// WriteCatalog records one scan of root's models beside them. Callers treat
// the error as best effort: a read-only root keeps its old snapshot and the
// scan itself has already happened.
func WriteCatalog(root string, models []Model) error {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	c := Catalog{Root: root, Host: host, At: time.Now().UTC()}
	for _, m := range models {
		c.Models = append(c.Models, CatalogModel{Name: m.Name, Path: m.Path, Shelf: m.Shelf, Bytes: m.Bytes})
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	p := CatalogPath(root)
	return fsx.WriteFile(p, append(b, '\n'), 0o644)
}

// ReadCatalog reads root's snapshot. A root that was never scanned has no
// catalog: os.ErrNotExist, which a caller reports as "no catalog".
func ReadCatalog(root string) (Catalog, error) {
	var c Catalog
	b, err := os.ReadFile(CatalogPath(root))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", CatalogName, err)
	}
	return c, nil
}

// CatalogAge is how long ago root's snapshot was written, or an error when
// it has none.
func CatalogAge(root string) (time.Duration, error) {
	c, err := ReadCatalog(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	if c.At.IsZero() {
		return 0, fmt.Errorf("catalog has no timestamp")
	}
	return time.Since(c.At), nil
}
