// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeRecord(t *testing.T, dir string, m Manifest) {
	t.Helper()
	if err := WriteManifest(dir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, Manifest{
		Source: SourceCivitai,
		Name:   "Flux Lora",
		Ref:    "12345",
		Files:  []File{{Path: "flux.safetensors", Size: 1 << 30, SHA256: "ab"}},
		Pulled: time.Now().UTC(),
	})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Source != SourceCivitai || m.Name != "Flux Lora" || m.Ref != "12345" {
		t.Errorf("round trip = %+v, want source/name/ref kept", m)
	}
	if len(m.Files) != 1 || m.Files[0].SHA256 != "ab" {
		t.Errorf("Files = %+v, want the one file with its hash", m.Files)
	}
}

// A record written by an older release has repo, revision and commit and no
// source: it must still read as the Hub pull it is.
func TestReadLegacyRecord(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"repo":"org/repo","revision":"main","commit":"abc123","files":[{"path":"m.gguf","size":100}],"pulled":"2026-10-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, Name), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m.Source != SourceHF || m.Name != "org/repo" || m.Ref != "abc123" {
		t.Errorf("legacy = %+v, want an hf record named org/repo at abc123", m)
	}
	// A new pull of the same repo merges into the legacy record, not over it.
	writeRecord(t, dir, Manifest{Source: SourceHF, Name: "org/repo", Ref: "def456",
		Files: []File{{Path: "n.gguf", Size: 1, SHA256: "cd"}}})
	m, err = ReadManifest(dir)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if len(m.Files) != 2 {
		t.Errorf("merged Files = %+v, want the legacy file plus the new one", m.Files)
	}
}

func TestWriteMergesQuantOfSameRecord(t *testing.T) {
	dir := t.TempDir()
	writeRecord(t, dir, Manifest{Source: SourceHF, Name: "org/repo", Ref: "c1",
		Files: []File{{Path: "q4.gguf", Size: 1}}})
	writeRecord(t, dir, Manifest{Source: SourceHF, Name: "org/repo", Ref: "c1",
		Files: []File{{Path: "q8.gguf", Size: 2}}})
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 {
		t.Errorf("Files = %+v, want both quants kept", m.Files)
	}
}

func TestVerifyStatuses(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.bin")
	if err := os.WriteFile(good, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := HashFile(good, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeRecord(t, dir, Manifest{Source: SourceHF, Name: "r", Files: []File{
		{Path: "good.bin", Size: 6, SHA256: sum},
		{Path: "gone.bin", Size: 5},
		{Path: "short.bin", Size: 9},
		{Path: "plain.bin", Size: 2},
	}})
	// short.bin exists but smaller than the record says.
	if err := os.WriteFile(filepath.Join(dir, "short.bin"), []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain.bin"), []byte("12"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks, err := Verify(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range checks {
		got[c.Path] = c.Status
	}
	want := map[string]string{"good.bin": "ok", "gone.bin": "missing", "short.bin": "size", "plain.bin": "unhashed"}
	for p, s := range want {
		if got[p] != s {
			t.Errorf("Verify(%s) = %q, want %q", p, got[p], s)
		}
	}
}

// A file whose hash no longer matches is reported as such, not as ok.
func TestVerifyHashMismatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(p, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeRecord(t, dir, Manifest{Source: SourceHF, Name: "r", Files: []File{{Path: "m.bin", Size: 6, SHA256: "0000"}}})
	checks, err := Verify(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Status != "hash" || !strings.Contains(checks[0].Detail, "want 0000") {
		t.Errorf("checks = %+v, want the one hash mismatch", checks)
	}
}

func TestVerifyNoRecord(t *testing.T) {
	if _, err := Verify(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), Name) {
		t.Errorf("Verify = %v, want it to name the missing record", err)
	}
}
