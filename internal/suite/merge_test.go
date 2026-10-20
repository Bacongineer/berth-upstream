// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergedSetup writes a temp berth.toml with one user engine and returns its
// path; the suite state file, when wanted, is written beside it.
func mergedSetup(t *testing.T, enginesTOML string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "berth.toml")
	if err := os.WriteFile(path, []byte(enginesTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeState(t *testing.T, cfgPath, stateTOML string) {
	t.Helper()
	if err := os.WriteFile(SuitePath(cfgPath), []byte(stateTOML), 0o644); err != nil {
		t.Fatal(err)
	}
}

const userEngineTOML = `
[[engine]]
name = "llama-swap"
type = "llama-swap"
url  = "http://127.0.0.1:9090"
`

const installStateTOML = `
[[install]]
name = "comfyui"
kind = "comfyui"
path = "/data/comfyui"
port = 8188
priority = "batch"

  [[install.component]]
  name = "comfyui"
  commit = "1a2b3c4d5e6f"
`

func TestMergedWithoutAStateFileChangesNothing(t *testing.T) {
	path := mergedSetup(t, userEngineTOML)
	cfg, warns, err := Merged(path)
	if err != nil {
		t.Fatalf("merged: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if len(cfg.Engine) != 1 || cfg.Engine[0].Name != "llama-swap" {
		t.Errorf("engines = %+v, want the file's one engine", cfg.Engine)
	}
}

func TestMergedAppendsTheDerivedEngines(t *testing.T) {
	path := mergedSetup(t, userEngineTOML)
	writeState(t, path, installStateTOML)
	cfg, warns, err := Merged(path)
	if err != nil {
		t.Fatalf("merged: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v, want none", warns)
	}
	if len(cfg.Engine) != 2 {
		t.Fatalf("engines = %d, want the file's plus the derived one", len(cfg.Engine))
	}
	derived := cfg.Engine[1]
	if derived.Name != "comfyui" || derived.Type != "comfyui" || derived.URL != "http://127.0.0.1:8188" {
		t.Errorf("derived engine = %+v", derived)
	}
	if derived.DrainComfyUI != derived.URL || len(derived.Start) == 0 {
		t.Errorf("the derived engine lacks the drain and start lines: %+v", derived)
	}
}

func TestMergedDropsAShadowedFileEngineWithAWarning(t *testing.T) {
	toml := `
[[engine]]
name = "comfyui"
type = "comfyui"
url  = "http://127.0.0.1:8188"
`
	path := mergedSetup(t, toml)
	writeState(t, path, installStateTOML)
	cfg, warns, err := Merged(path)
	if err != nil {
		t.Fatalf("merged: %v", err)
	}
	shadowed := 0
	for _, w := range warns {
		if strings.Contains(w.Message, "managed by the suite") {
			shadowed++
			if !strings.Contains(w.Message, "comfyui") || !strings.Contains(w.Message, "dropped") {
				t.Errorf("warning = %+v, want it to name the install and the drop", w)
			}
		}
	}
	if shadowed != 1 {
		t.Fatalf("shadow warnings = %d, want one (all: %v)", shadowed, warns)
	}
	if len(cfg.Engine) != 1 || cfg.Engine[0].Name != "comfyui" {
		t.Fatalf("engines = %+v, want just the derived one", cfg.Engine)
	}
	if len(cfg.Engine[0].Start) == 0 {
		t.Error("the surviving engine is the hand-written one, not the derived engine")
	}
}

func TestMergedWithACorruptStateFileIsAnError(t *testing.T) {
	path := mergedSetup(t, userEngineTOML)
	writeState(t, path, "install = [unclosed")
	if _, _, err := Merged(path); err == nil {
		t.Fatal("a corrupt state file merged without an error")
	}
}

func TestMergedWithAnInvalidStateFileIsAnError(t *testing.T) {
	path := mergedSetup(t, userEngineTOML)
	writeState(t, path, strings.Replace(installStateTOML, "batch", "urgent", 1))
	if _, _, err := Merged(path); err == nil {
		t.Fatal("an invalid state file merged without an error")
	}
}
