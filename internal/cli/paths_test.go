// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A shell without XDG_RUNTIME_DIR (ssh, a tool's shell) finds the socket the
// user service listens on, in the session's runtime directory, and falls back
// to the temp dir only when the user has no session directory.
func TestDefaultSocketWithoutXDGRuntimeDir(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the session runtime directory is Linux's")
	}
	t.Setenv("BERTH_SOCKET", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	root := t.TempDir()
	old := userRuntimeRoot
	userRuntimeRoot = root
	t.Cleanup(func() { userRuntimeRoot = old })

	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("berth-%d.sock", os.Getuid()))
	if got := defaultSocket(); got != tmp {
		t.Fatalf("without a session directory: %s, want %s", got, tmp)
	}
	dir := filepath.Join(root, fmt.Sprint(os.Getuid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := defaultSocket(), filepath.Join(dir, "berth.sock"); got != want {
		t.Fatalf("with the session directory: %s, want %s", got, want)
	}
}
