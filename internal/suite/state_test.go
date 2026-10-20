// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func twoInstallState() State {
	return State{
		Installs: []Install{
			{
				Name: "comfyui", Kind: KindComfyUI, Path: "/home/op/ComfyUI", Port: 8188,
				Priority: "batch", Venv: ".venv",
				Components: []Component{
					{Name: "comfyui", Repo: "https://github.com/comfyanonymous/ComfyUI.git", Channel: "master",
						Commit: "1a2b3c4d5e6f", Previous: "0f9e8d7c6b5a"},
					{Name: "ComfyUI-Manager", Repo: "https://github.com/Comfy-Org/ComfyUI-Manager.git",
						Channel: "main", Path: "custom_nodes/ComfyUI-Manager", Commit: "deadbeef1234"},
				},
			},
			{
				Name: "comfyui-nightly", Kind: KindComfyUI, Path: "/data/comfy", Port: 8189, Priority: "interactive",
				Components: []Component{
					{Name: "comfyui", Repo: "https://github.com/comfyanonymous/ComfyUI.git", Channel: "master", Commit: "abc1234"},
				},
			},
		},
	}
}

func TestStateTOMLRoundTrip(t *testing.T) {
	st := twoInstallState()
	if err := st.Validate(); err != nil {
		t.Fatalf("valid state failed validation: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "engines.toml")
	if err := Save(path, st); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o644 {
		t.Errorf("state file mode = %o, want 0644", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{"[[install]]", "name = \"comfyui\"", "[[install.component]]",
		"name = \"ComfyUI-Manager\"", "path = \"custom_nodes/ComfyUI-Manager\"", "port = 8189"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("state file lacks %q:\n%s", want, raw)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped state failed validation: %v", err)
	}
	if len(got.Installs) != 2 {
		t.Fatalf("installs = %d, want 2", len(got.Installs))
	}
	if got.Installs[0].Components[1].Commit != "deadbeef1234" || got.Installs[0].Components[0].Previous != "0f9e8d7c6b5a" {
		t.Errorf("components did not survive the round trip: %+v", got.Installs[0].Components)
	}
	if got.Installs[1].Port != 8189 || got.Installs[1].Priority != "interactive" {
		t.Errorf("second install = %+v", got.Installs[1])
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if len(st.Installs) != 0 {
		t.Errorf("installs = %d, want 0", len(st.Installs))
	}
	if err := st.Validate(); err != nil {
		t.Errorf("empty state: %v", err)
	}
}

func TestLoadCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engines.toml")
	if err := os.WriteFile(path, []byte("install = [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("corrupt file loaded without an error")
	}
}

func validInstall(name string, port int) Install {
	return Install{Name: name, Kind: KindComfyUI, Path: "/tmp/" + name, Port: port, Priority: "batch",
		Components: []Component{{Name: "comfyui", Repo: "https://github.com/comfyanonymous/ComfyUI.git",
			Channel: "master", Commit: "1a2b3c4"}}}
}

func TestValidateRules(t *testing.T) {
	cases := []struct {
		name  string
		state State
		want  string
	}{
		{"duplicate name", State{Installs: []Install{validInstall("a", 8188), validInstall("a", 8189)}}, "duplicate name"},
		{"bad name", State{Installs: []Install{{Name: "Bad_Name", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "comfyui", Commit: "1a2b3c4"}}}}}, "name must be lowercase"},
		{"port collision", State{Installs: []Install{validInstall("a", 8188), validInstall("b", 8188)}}, "already used by"},
		{"port too low", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 80, Priority: "batch",
			Components: []Component{{Name: "comfyui", Commit: "1a2b3c4"}}}}}, "not in 1024..65535"},
		{"port too high", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 70000, Priority: "batch",
			Components: []Component{{Name: "comfyui", Commit: "1a2b3c4"}}}}}, "not in 1024..65535"},
		{"missing core", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "somepack", Path: "custom_nodes/somepack", Commit: "1a2b3c4"}}}}}, "exactly one component"},
		{"core off root", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "comfyui", Path: "core", Commit: "1a2b3c4"}}}}}, "install root"},
		{"duplicate component", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{
				{Name: "comfyui", Commit: "1a2b3c4"},
				{Name: "pack", Path: "custom_nodes/pack", Commit: "2b3c4d5"},
				{Name: "pack", Path: "custom_nodes/pack2", Commit: "3c4d5e6"},
			}}}}, "duplicate component name"},
		{"bad priority", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "urgent",
			Components: []Component{{Name: "comfyui", Commit: "1a2b3c4"}}}}}, "not batch, interactive or foreground"},
		{"bad kind", State{Installs: []Install{{Name: "a", Kind: "llamacpp", Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "comfyui", Commit: "1a2b3c4"}}}}}, "not comfyui"},
		{"bad sha", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "comfyui", Commit: "xyz"}}}}}, "not a hex sha"},
		{"sha too short", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "comfyui", Commit: "abc12"}}}}}, "not a hex sha"},
		{"bad repo", State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
			Components: []Component{{Name: "comfyui", Repo: "ftp://example.com/x.git", Commit: "1a2b3c4"}}}}}, "http(s) URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.state.Validate()
			if err == nil {
				t.Fatalf("Validate passed, want an error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsEmptyRepoAndPrevious(t *testing.T) {
	// A checkout without an origin remote records an empty repo, and a
	// freshly adopted component has no previous commit yet: both are valid.
	st := State{Installs: []Install{{Name: "a", Kind: KindComfyUI, Path: "/p", Port: 8188, Priority: "batch",
		Components: []Component{
			{Name: "comfyui", Repo: "", Channel: "", Commit: "1a2b3c4"},
			{Name: "pack", Repo: "", Channel: "", Path: "custom_nodes/pack", Commit: "2b3c4d5"},
		}}}}
	if err := st.Validate(); err != nil {
		t.Errorf("empty repo and previous are valid: %v", err)
	}
}
