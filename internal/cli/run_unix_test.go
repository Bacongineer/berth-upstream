// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With no command, berth run starts the entry's own start, as the gateway
// does (#232). A start found only through --group is another engine's (the
// llama-swap server, not one model), so it is never run for --as.
func TestRunWithNoCommandUsesTheEntrysStart(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	toml := filepath.Join(home, "berth.toml")
	started := filepath.Join(home, "started")
	file := "" +
		"[[engine]]\n" +
		"name = \"tagger\"\n" +
		"type = \"custom\"\n" +
		"url = \"http://127.0.0.1:1234\"\n" +
		"health = \"/\"\n" +
		"background = true\n" +
		"start = [\"touch\", '" + started + "']\n"
	if err := os.WriteFile(toml, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "nope.sock")

	code, _, errOut := runCLI(t, "run", "--as", "other", "--group", "tagger", "--socket", missing, "--config", toml)
	if code != ExitSystem || !strings.Contains(errOut, "--as NAME and the engine's command are required") {
		t.Errorf("start through --group: exit %d, stderr %q, want the command required", code, errOut)
	}
	if _, err := os.Stat(started); err == nil {
		t.Fatal("the group entry's start ran for another engine")
	}

	code, _, errOut = runCLI(t, "run", "--as", "tagger", "--socket", missing, "--config", toml)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(started); err != nil {
		t.Errorf("the entry's start did not run: %v", err)
	}
}
