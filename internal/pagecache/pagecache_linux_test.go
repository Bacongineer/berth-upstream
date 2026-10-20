// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pagecache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResidentCountsReadPages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	data := make([]byte, 4<<20) // 4 MiB: enough pages to measure
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// The write and the read both leave the file's pages in the cache.
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	cached, size, err := Resident(path)
	if err != nil {
		t.Fatalf("Resident: %v", err)
	}
	if size != int64(len(data)) {
		t.Errorf("size = %d, want %d", size, len(data))
	}
	if cached <= 0 {
		t.Errorf("cached = %d after a full read, want more than 0", cached)
	}
	if cached > int64(len(data)) {
		t.Errorf("cached = %d over a %d-byte file", cached, len(data))
	}
}

func TestResidentMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.bin")
	if _, _, err := Resident(path); err == nil {
		t.Errorf("Resident(%q) err = nil, want an error for a missing file", path)
	}
}

func TestResidentZeroByteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cached, size, err := Resident(path)
	if err != nil {
		t.Fatalf("Resident: %v", err)
	}
	if cached != 0 || size != 0 {
		t.Errorf("(cached, size) = (%d, %d), want (0, 0)", cached, size)
	}
}

// TestResidentSlicesLargeFiles proves the check walks a file over several
// GiB in bounded slices: a sparse file costs no disk and no RAM, and the
// check must finish it without error or memory growth.
func TestResidentSlicesLargeFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sparse.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(2 << 30); err != nil { // 2 GiB of holes
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cached, size, err := Resident(path)
	if err != nil {
		t.Fatalf("Resident: %v", err)
	}
	if size != 2<<30 {
		t.Errorf("size = %d, want %d", size, int64(2)<<30)
	}
	if cached != 0 {
		t.Errorf("cached = %d for a file nothing has read, want 0", cached)
	}
}

func TestPrefetchReturnsAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(path, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prefetch(path); err != nil {
		t.Errorf("Prefetch: %v", err)
	}
}

func TestPrefetchMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.bin")
	if err := Prefetch(path); err == nil {
		t.Errorf("Prefetch(%q) err = nil, want an error for a missing file", path)
	}
}

func TestPrefetchZeroByteFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Prefetch(path); err != nil {
		t.Errorf("Prefetch on an empty file: %v", err)
	}
}
