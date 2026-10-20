// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package models

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCacheSumsModelAndCompanions is the multi-file case: a model's files and
// the projector that goes with it are measured together, so the model's share
// covers everything a restart would read.
func TestCacheSumsModelAndCompanions(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "Test-8B-Q4_K_M.gguf")
	proj := filepath.Join(dir, "Test-8B-mmproj-f16.gguf")
	mSize := sampleFile(t, model, "Test 8B")
	pSize := sampleFile(t, proj, "Test 8B vision")
	ms := Scan([]string{dir})
	Cache(ms)
	m, ok := byPath(ms, model)
	if !ok {
		t.Fatalf("the model is not in the scan")
	}
	if m.CachedSize != uint64(mSize+pSize) {
		t.Errorf("cached size = %d, want the model and its projector (%d)", m.CachedSize, mSize+pSize)
	}
	if m.CachedBytes == 0 {
		t.Errorf("cached bytes = 0 after both files were just read, want more")
	}
	if share := m.CachedShare(); share < 90 {
		t.Errorf("share = %d%% for a just-read file, want at least 90", share)
	}
	if m.Warmth() != "COOL" {
		t.Errorf("warmth = %q for a fully cached model, want COOL", m.Warmth())
	}
	// The projector's own row measures itself and nothing else.
	p, ok := byPath(ms, proj)
	if !ok {
		t.Fatalf("the projector is not in the scan")
	}
	if p.CachedSize != uint64(pSize) {
		t.Errorf("projector cached size = %d, want its own %d", p.CachedSize, pSize)
	}
}

// TestCacheLeavesUnreachableFilesUnknown is the half-downloaded case: the
// missing part is reported on the model, and the share is measured over what
// the check can reach rather than failing the model.
func TestCacheLeavesUnreachableFilesUnknown(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "Split-Q4-00001-of-00002.gguf")
	p1Size := sampleFile(t, p1, "Split")
	ms := Scan([]string{dir})
	Cache(ms)
	m, ok := byPath(ms, p1)
	if !ok {
		t.Fatalf("the split model is not in the scan")
	}
	if m.Err == "" {
		t.Errorf("the missing second part is not reported")
	}
	if m.CachedSize != uint64(p1Size) {
		t.Errorf("cached size = %d, want the one part that is there (%d)", m.CachedSize, p1Size)
	}
	if m.CachedShare() < 0 {
		t.Errorf("share = -1, want the reachable part measured")
	}
}

// TestCacheUnknownWhenNothingMeasured: a model whose files the check cannot
// reach has no share at all, which a listing reads as a dash, not as 0%.
func TestCacheUnknownWhenNothingMeasured(t *testing.T) {
	ms := []Model{{
		Name: "ghost",
		Path: filepath.Join(t.TempDir(), "gone.gguf"),
	}}
	Cache(ms)
	if share := ms[0].CachedShare(); share != -1 {
		t.Errorf("share = %d for an unmeasured model, want -1", share)
	}
	if warmth := ms[0].Warmth(); warmth != "" {
		t.Errorf("warmth = %q for an unmeasured model, want none", warmth)
	}
}

// TestCacheEmptyFileMeasuresNothing: a file with no pages has no share, which
// is the difference between "cold" and "unknown".
func TestCacheEmptyFileMeasuresNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.gguf")
	writeGGUF(t, p, nil)
	ms := []Model{{Name: "empty", Path: p, Files: []string{p}}}
	Cache(ms)
	if share := ms[0].CachedShare(); share != -1 {
		t.Errorf("share = %d for an empty file, want -1", share)
	}
}

// makeSparse is a file with a size and no pages behind it: the cheapest way
// to name a file nothing has read.
func makeSparse(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// TestCacheColdBelowTheWarmLine needs a file the kernel will not hold: a
// freshly written sparse file that nothing has read. Its pages are not in the
// cache, so the share is 0 and the model reads as COLD.
func TestCacheColdBelowTheWarmLine(t *testing.T) {
	ms := []Model{{
		Name:   "sparse",
		Path:   filepath.Join(t.TempDir(), "sparse.bin"),
		Files:  nil,
	}}
	if err := makeSparse(ms[0].Path, 4<<20); err != nil {
		t.Fatal(err)
	}
	Cache(ms)
	if share := ms[0].CachedShare(); share != 0 {
		t.Errorf("share = %d for a file nothing has read, want 0", share)
	}
	if warmth := ms[0].Warmth(); warmth != "COLD" {
		t.Errorf("warmth = %q for an uncached file, want COLD", warmth)
	}
}
