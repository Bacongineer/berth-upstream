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
	if _, changed, err := Install(dir); err != nil || !changed {
		t.Fatalf("first install: changed=%v err=%v", changed, err)
	}
	if b, _ := os.ReadFile(dst + ".bak"); string(b) != "old" {
		t.Fatalf("backup = %q", b)
	}
	if _, changed, err := Install(dir); err != nil || changed {
		t.Fatalf("second install: changed=%v err=%v", changed, err)
	}
}

// The skill must load in agents: front matter with a name and a description.
func TestDocFrontMatter(t *testing.T) {
	s := string(Doc)
	if !strings.HasPrefix(s, "---\nname: berth\ndescription: ") || !strings.Contains(s[4:], "\n---\n") {
		t.Fatal("SKILL.md front matter is malformed")
	}
}
