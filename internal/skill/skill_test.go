// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallIsIdempotentAndKeepsTheOld(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "berth", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, changed, err := Install(dir, Skills[0]); err != nil || !changed {
		t.Fatalf("first install: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(dst + ".bak"); string(b) != "old" {
		t.Fatalf("backup = %q", b)
	}
	if _, changed, err := Install(dir, Skills[0]); err != nil || changed {
		t.Fatalf("second install: changed=%v err=%v", changed, err)
	}
}

// Every skill must load in agents: front matter whose name is the folder
// it installs into, and a description.
func TestDocFrontMatter(t *testing.T) {
	for _, sk := range Skills {
		s := string(sk.Doc)
		if !strings.HasPrefix(s, "---\nname: "+sk.Name+"\ndescription: ") || !strings.Contains(s[4:], "\n---\n") {
			t.Errorf("%s/SKILL.md front matter is malformed", sk.Name)
		}
	}
}

// Install writes each skill under its own folder.
func TestInstallWritesEachSkillInItsFolder(t *testing.T) {
	dir := t.TempDir()
	for _, sk := range Skills {
		path, _, err := Install(dir, sk)
		if err != nil || path != filepath.Join(dir, sk.Name, "SKILL.md") {
			t.Fatalf("install %s = %s %v", sk.Name, path, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "comfyui", "SKILL.md"))
	if err != nil || !strings.HasPrefix(string(got), "---\nname: comfyui\n") {
		t.Fatalf("comfyui/SKILL.md = %.40q %v", got, err)
	}
}

func TestHermesTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HERMES_HOME", "")
	t.Setenv("LOCALAPPDATA", "")
	if got := strings.Join(All(home), ","); got != "agents,claude,opencode" {
		t.Fatalf("all without a Hermes home = %s", got)
	}
	if err := os.Mkdir(filepath.Join(home, ".hermes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(All(home), ","); got != "agents,claude,hermes,opencode" {
		t.Fatalf("all with ~/.hermes = %s", got)
	}
	if dir, _ := Dir("hermes", home); dir != filepath.Join(home, ".hermes", "skills", "mlops") {
		t.Fatalf("hermes dir = %s", dir)
	}
	profile := filepath.Join(home, "profiles", "coder")
	t.Setenv("HERMES_HOME", profile)
	if dir, _ := Dir("hermes", home); dir != filepath.Join(profile, "skills", "mlops") {
		t.Fatalf("hermes dir under HERMES_HOME = %s", dir)
	}
	if _, ok := Dir("cursor", home); ok {
		t.Fatal("an unknown target resolved")
	}
}
