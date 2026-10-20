// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"path/filepath"
	"strconv"
	"testing"
)

// startTail is the derived start line after the berth binary: the berth run
// wrapper, then the ComfyUI command with the model-paths file named.
func startTail(installPath, venv, priority string, port int, keepUp bool) []string {
	py := "python3"
	if venv != "" {
		py = venvPython(installPath, venv)
	}
	tail := []string{"run", "--as", "comfyui", "--priority", priority, "--drain-comfyui",
		"http://127.0.0.1:" + strconv.Itoa(port)}
	if keepUp {
		tail = append(tail, "--keep-comfyui-up")
	}
	return append(tail, "--", py, "main.py", "--listen", "127.0.0.1",
		"--port", strconv.Itoa(port), "--extra-model-paths-config",
		filepath.Join(installPath, "extra_model_paths.yaml"))
}

func assertStart(t *testing.T, got []string, installPath, venv, priority string, port int, keepUp bool) {
	t.Helper()
	want := startTail(installPath, venv, priority, port, keepUp)
	if len(got) != len(want)+1 {
		t.Fatalf("start line has %d elements, want %d:\n%v", len(got), len(want)+1, got)
	}
	if got[0] == "" {
		t.Error("the start line does not begin with a berth binary")
	}
	for i, w := range want {
		if got[i+1] != w {
			t.Errorf("start[%d] = %q, want %q\nfull: %v", i+1, got[i+1], w, got)
			return
		}
	}
}

func TestEngineForAVenvInstall(t *testing.T) {
	dir := t.TempDir()
	e := Engine(Install{Name: "comfyui", Kind: KindComfyUI, Path: dir, Port: 8188,
		Priority: "batch", Venv: ".venv"})
	if e.Type != "comfyui" || e.Kind != "image" {
		t.Errorf("type/kind = %s/%s", e.Type, e.Kind)
	}
	if e.URL != "http://127.0.0.1:8188" || e.GUI != e.URL {
		t.Errorf("url = %s, gui = %s", e.URL, e.GUI)
	}
	if e.DrainComfyUI != e.URL {
		t.Errorf("drain_comfyui = %s, want the engine url", e.DrainComfyUI)
	}
	if e.KeepComfyUI {
		t.Error("keep_comfyui_up set without the install asking for it")
	}
	if e.Name != "comfyui" || e.Priority != "batch" {
		t.Errorf("name/priority = %s/%s", e.Name, e.Priority)
	}
	assertStart(t, e.Start, dir, ".venv", "batch", 8188, false)
}

func TestEngineKeepsTheServerUpWhenTheInstallSaysSo(t *testing.T) {
	dir := t.TempDir()
	e := Engine(Install{Name: "comfyui", Kind: KindComfyUI, Path: dir, Port: 8189,
		Priority: "interactive", KeepUp: true, Venv: ".venv"})
	if !e.KeepComfyUI {
		t.Error("keep_comfyui_up not set")
	}
	assertStart(t, e.Start, dir, ".venv", "interactive", 8189, true)
}

func TestEngineWithoutAVenvRunsPython3(t *testing.T) {
	dir := t.TempDir()
	e := Engine(Install{Name: "comfyui", Kind: KindComfyUI, Path: dir, Port: 8188,
		Priority: "batch"})
	i := 0
	for j, s := range e.Start {
		if s == "--" {
			i = j
		}
	}
	if e.Start[i+1] != "python3" {
		t.Errorf("the python of a venv-less install = %q, want python3 (full: %v)", e.Start[i+1], e.Start)
	}
	assertStart(t, e.Start, dir, "", "batch", 8188, false)
}
