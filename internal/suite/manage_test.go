// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeShell records every step; a git clone materializes its target as a
// checkout, the effect the real step has on disk.
type fakeShell struct {
	t     *testing.T
	calls []string
	fail  string
}

func (f *fakeShell) Run(bin string, dir string, args ...string) error {
	joined := bin + " " + strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	if f.fail != "" && strings.Contains(joined, f.fail) {
		return fmt.Errorf("simulated failure in %q", joined)
	}
	if bin == "git" && len(args) >= 3 && args[0] == "clone" {
		checkoutTree(f.t, args[len(args)-1])
	}
	return nil
}

// checkoutTree builds a stand-in ComfyUI checkout: main.py, the .git marker,
// a venv, and custom_nodes holding one git pack and one plain directory.
func checkoutTree(t *testing.T, dir string) {
	t.Helper()
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.py")
	write(".git/HEAD")
	write(".venv/pyvenv.cfg")
	write("requirements.txt")
	write("custom_nodes/ComfyUI-Manager/.git/HEAD")
	write("custom_nodes/ComfyUI-Manager/main.py")
	write("custom_nodes/a-plain-pack/nodes.py")
}

func adoptSetup(t *testing.T) (*fakeGit, *Manager, string) {
	t.Helper()
	dir := t.TempDir()
	checkoutTree(t, dir)
	g := newFakeGit()
	g.set(dir, shaA, "master", "https://github.com/comfyanonymous/ComfyUI.git", map[string]string{})
	g.dirty[dir] = true
	mgr := filepath.Join(dir, "custom_nodes", "ComfyUI-Manager")
	g.set(mgr, shaB, "main", "https://github.com/Comfy-Org/ComfyUI-Manager.git", map[string]string{})
	return g, &Manager{Git: g}, dir
}

func TestAdoptRecordsCorePacksAndVenv(t *testing.T) {
	_, m, dir := adoptSetup(t)
	inst, err := m.Adopt("comfyui", dir, 8188, "batch", true)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if inst.Name != "comfyui" || inst.Kind != KindComfyUI || inst.Port != 8188 ||
		inst.Priority != "batch" || !inst.KeepUp {
		t.Errorf("install fields = %+v", inst)
	}
	if inst.Venv != ".venv" {
		t.Errorf("venv = %q, want .venv", inst.Venv)
	}
	if len(inst.Components) != 2 {
		t.Fatalf("components = %d, want 2 (the core and the git pack; the plain directory is not recorded)", len(inst.Components))
	}
	core := inst.Components[0]
	if core.Name != KindComfyUI || core.Path != "" || core.Commit != shaA || core.Channel != "master" ||
		core.Repo != "https://github.com/comfyanonymous/ComfyUI.git" || !core.Dirty {
		t.Errorf("core = %+v", core)
	}
	pack := inst.Components[1]
	if pack.Name != "ComfyUI-Manager" || pack.Path != "custom_nodes/ComfyUI-Manager" ||
		pack.Commit != shaB || pack.Channel != "main" {
		t.Errorf("pack = %+v", pack)
	}
}

func TestAdoptWithoutAVenvLeavesItEmpty(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.py")
	write(".git/HEAD")
	g := newFakeGit()
	g.set(dir, shaA, "master", "", map[string]string{})
	m := &Manager{Git: g}
	inst, err := m.Adopt("comfyui", dir, 8188, "batch", false)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if inst.Venv != "" {
		t.Errorf("venv = %q, want empty", inst.Venv)
	}
}

func TestAdoptRefusesANotAComfyUIPath(t *testing.T) {
	m := &Manager{Git: newFakeGit()}
	_, err := m.Adopt("comfyui", filepath.Join(t.TempDir(), "nowhere"), 8188, "batch", false)
	if err == nil || !strings.Contains(err.Error(), "no such directory") {
		t.Errorf("error = %v, want no such directory", err)
	}

	noMain := t.TempDir()
	if err := os.MkdirAll(filepath.Join(noMain, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noMain, ".git", "HEAD"), []byte("ref: refs/heads/master"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.Adopt("comfyui", noMain, 8188, "batch", false)
	if err == nil || !strings.Contains(err.Error(), "main.py") {
		t.Errorf("error = %v, want it to name main.py", err)
	}

	noGit := t.TempDir()
	if err := os.WriteFile(filepath.Join(noGit, "main.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = m.Adopt("comfyui", noGit, 8188, "batch", false)
	if err == nil || !strings.Contains(err.Error(), ".git") {
		t.Errorf("error = %v, want it to name the missing .git", err)
	}
}

func TestInstallRunsCloneVenvPipInOrderAndRecordsPacks(t *testing.T) {
	shell := &fakeShell{t: t}
	g := newFakeGit()
	target := filepath.Join(t.TempDir(), "fresh")
	// the fakes learn about the checkouts the fake clone creates: the core
	// and the pack the tree ships, plus the pack the install clones
	g.set(target, shaA, "master", "https://github.com/comfyanonymous/ComfyUI.git", map[string]string{})
	g.set(filepath.Join(target, "custom_nodes", "ComfyUI-Manager"), shaB, "main",
		"https://github.com/Comfy-Org/ComfyUI-Manager.git", map[string]string{})
	g.set(filepath.Join(target, "custom_nodes", "deforum-stable"), shaC, "master", "", map[string]string{})
	m := &Manager{Git: g, Shell: shell}
	inst, err := m.Install("comfyui", InstallOpts{
		Path: target, Port: 8189, Priority: "interactive",
		Packs: []string{"https://github.com/deforum/deforum-stable"},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(inst.Components) != 3 {
		t.Fatalf("components = %d, want 3 (core, the manager the tree ships, the cloned pack)", len(inst.Components))
	}
	var names []string
	for _, c := range inst.Components {
		names = append(names, c.Name)
	}
	for _, want := range []string{KindComfyUI, "ComfyUI-Manager", "deforum-stable"} {
		if !containsString(names, want) {
			t.Errorf("components %v lack %q", names, want)
		}
	}
	if inst.Venv != ".venv" {
		t.Errorf("venv = %q, want .venv", inst.Venv)
	}
	// the steps run in order: the clone, the venv, the requirements through
	// the venv's python, the pack clone
	joined := strings.Join(shell.calls, "\n")
	positions := []int{}
	for _, want := range []string{
		"git clone --branch master https://github.com/comfyanonymous/ComfyUI.git " + target,
		"python3 -m venv .venv",
		"-m pip install -r requirements.txt",
		"git clone https://github.com/deforum/deforum-stable",
	} {
		i := strings.Index(joined, want)
		if i < 0 {
			t.Fatalf("calls %v lack %q", shell.calls, want)
		}
		positions = append(positions, i)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] < positions[i-1] {
			t.Errorf("calls out of order: %v", shell.calls)
		}
	}
}

func TestInstallStepFailureStopsTheInstall(t *testing.T) {
	shell := &fakeShell{t: t, fail: "venv"}
	m := &Manager{Git: newFakeGit(), Shell: shell}
	_, err := m.Install("comfyui", InstallOpts{Path: filepath.Join(t.TempDir(), "x"), Port: 8189})
	if err == nil {
		t.Fatal("install passed though the venv step failed")
	}
	if !strings.Contains(err.Error(), "venv") {
		t.Errorf("error = %v, want it to name the venv step", err)
	}
}

func TestAddPacksSkipsOneAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	checkoutTree(t, dir)
	g := newFakeGit()
	mgr := filepath.Join(dir, "custom_nodes", "ComfyUI-Manager")
	g.set(mgr, shaB, "main", "https://github.com/Comfy-Org/ComfyUI-Manager.git", map[string]string{})
	shell := &fakeShell{t: t}
	m := &Manager{Git: g, Shell: shell}
	inst := &Install{Path: dir, Venv: ".venv"}
	if err := m.AddPacks(inst, []string{"https://github.com/Comfy-Org/ComfyUI-Manager"}); err != nil {
		t.Fatalf("add packs: %v", err)
	}
	if len(shell.calls) != 0 {
		t.Errorf("calls = %v, want none: the pack is already present", shell.calls)
	}
	if len(inst.Components) != 0 {
		t.Errorf("components = %+v, want none recorded", inst.Components)
	}
}

func TestLastSegment(t *testing.T) {
	cases := map[string]string{
		"https://github.com/Comfy-Org/ComfyUI-Manager":  "ComfyUI-Manager",
		"https://github.com/deforum/deforum-stable.git": "deforum-stable",
		"https://github.com/Comfy-Org/ComfyUI-Manager/": "ComfyUI-Manager",
		"git@github.com:Comfy-Org/ComfyUI-Manager.git":  "ComfyUI-Manager",
	}
	for in, want := range cases {
		if got := lastSegment(in); got != want {
			t.Errorf("lastSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRemoveDropsTheEntry(t *testing.T) {
	dir := t.TempDir()
	st := State{Installs: []Install{
		{Name: "a", Path: dir},
		{Name: "b", Path: filepath.Join(t.TempDir(), "b")},
	}}
	if err := Remove(&st, "a", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(st.Installs) != 1 || st.Installs[0].Name != "b" {
		t.Errorf("installs = %+v, want just b", st.Installs)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the directory of an install removed without --delete must stay: %v", err)
	}
	if err := Remove(&st, "a", false); err == nil || !strings.Contains(err.Error(), "no install named") {
		t.Errorf("second remove = %v, want it to name the missing install", err)
	}
}

func TestRemoveWithDeleteDeletesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := State{Installs: []Install{{Name: "a", Path: dir}}}
	if err := Remove(&st, "a", true); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(st.Installs) != 0 {
		t.Errorf("installs = %+v, want empty", st.Installs)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory %s still exists after remove --delete", dir)
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
