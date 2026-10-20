// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderModelPathsCoversEveryKeyWithTheExistingRoots(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(b, 0o755); err != nil {
		t.Fatal(err)
	}
	got := RenderModelPaths([]string{a, a, b, filepath.Join(dir, "missing")})
	for _, want := range []string{
		"# Written by Berth.",
		"berth:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered file lacks %q:\n%s", want, got)
		}
	}
	for i, k := range ModelPathKeys {
		header := "  " + k + ": |\n"
		start := strings.Index(got, header)
		if start < 0 {
			t.Errorf("rendered file lacks the %q section:\n%s", k, got)
			continue
		}
		start += len(header)
		end := len(got)
		if i+1 < len(ModelPathKeys) {
			if p := strings.Index(got[start:], "  "+ModelPathKeys[i+1]+": |\n"); p >= 0 {
				end = start + p
			}
		}
		block := got[start:end]
		for _, r := range []string{"    " + a, "    " + b} {
			if !strings.Contains(block, r) {
				t.Errorf("the %q section lacks root %s:\n%s", k, r, block)
			}
		}
	}
	if strings.Contains(got, "missing") {
		t.Errorf("a missing root must be dropped:\n%s", got)
	}
}

func TestRenderModelPathsWithNoRootsWritesAnEmptySection(t *testing.T) {
	got := RenderModelPaths([]string{filepath.Join(t.TempDir(), "nope")})
	if !strings.Contains(got, "berth: {}") {
		t.Errorf("no roots: want an empty section, got:\n%s", got)
	}
}

func TestWriteModelPathsWritesIntoTheInstallDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "models")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := Install{Name: "comfyui", Path: filepath.Join(dir, "ComfyUI")}
	if err := WriteModelPaths(inst, []string{root}); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ComfyUI", "extra_model_paths.yaml"))
	if err != nil {
		t.Fatalf("the file is not there: %v", err)
	}
	if string(raw) != RenderModelPaths([]string{root}) {
		t.Errorf("file content differs from the render:\n%s", raw)
	}
}
