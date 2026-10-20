// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package skill carries the agent skill that teaches a coding agent to use
// Berth, and installs it where agents look for skills.
package skill

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed SKILL.md
var Doc []byte

// Targets are the skill directories the common agents read, under the
// user's home: Claude Code, the shared ~/.agents folder (Codex, Pi, OMP and
// others), and OpenCode.
var Targets = map[string]string{
	"claude":   ".claude/skills",
	"agents":   ".agents/skills",
	"opencode": ".config/opencode/skills",
}

// Install writes SKILL.md into dir/berth/. An existing file that differs is
// kept as SKILL.md.bak unless it is already the same.
func Install(dir string) (string, bool, error) {
	if dir == "" {
		return "", false, errors.New("no directory")
	}
	dst := filepath.Join(dir, "berth", "SKILL.md")
	if old, err := os.ReadFile(dst); err == nil {
		if string(old) == string(Doc) {
			return dst, false, nil
		}
		if err := os.WriteFile(dst+".bak", old, 0o644); err != nil {
			return dst, false, fmt.Errorf("back up %s: %w", dst, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return dst, false, err
	}
	if err := os.WriteFile(dst, Doc, 0o644); err != nil {
		return dst, false, err
	}
	return dst, true, nil
}
