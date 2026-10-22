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
	"runtime"
	"strings"
)

//go:embed SKILL.md
var Doc []byte

// Targets are the agents the skill installs for, in the order "all" lists
// them: the shared ~/.agents folder (Codex, Pi, OMP and others), Claude
// Code, Hermes Agent and OpenCode.
var Targets = []string{"agents", "claude", "hermes", "opencode"}

// Dir is target's skills directory for the user whose home is home; ok is
// false for an unknown target. Hermes reads skills by category under its
// own home, $HERMES_HOME when set, else %LOCALAPPDATA%\hermes on Windows
// and ~/.hermes elsewhere.
func Dir(target, home string) (dir string, ok bool) {
	switch target {
	case "agents":
		return filepath.Join(home, ".agents", "skills"), true
	case "claude":
		return filepath.Join(home, ".claude", "skills"), true
	case "opencode":
		return filepath.Join(home, ".config", "opencode", "skills"), true
	case "hermes":
		return filepath.Join(hermesHome(home), "skills", "mlops"), true
	}
	return "", false
}

// All is what "all" installs for: every target, Hermes only when its home
// exists, so the skill never creates ~/.hermes for someone without Hermes.
func All(home string) []string {
	var out []string
	for _, t := range Targets {
		if t == "hermes" {
			if fi, err := os.Stat(hermesHome(home)); err != nil || !fi.IsDir() {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

func hermesHome(home string) string {
	if h := strings.TrimSpace(os.Getenv("HERMES_HOME")); h != "" {
		return h
	}
	if d := os.Getenv("LOCALAPPDATA"); d != "" && runtime.GOOS == "windows" {
		return filepath.Join(d, "hermes")
	}
	return filepath.Join(home, ".hermes")
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
