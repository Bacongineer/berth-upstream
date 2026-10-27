// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// userRuntimeRoot holds each user's session runtime directory, the one
// systemd sets XDG_RUNTIME_DIR to.
var userRuntimeRoot = "/run/user"

// defaultSocket is $BERTH_SOCKET, else $XDG_RUNTIME_DIR/berth.sock, else a
// per-user file in the temp dir.
func defaultSocket() string {
	if s := os.Getenv("BERTH_SOCKET"); s != "" {
		return s
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "berth.sock")
	}
	// Windows (10 1803 and later has AF_UNIX): the per-user local app data
	// folder, whose ACL already keeps other users out.
	if d := os.Getenv("LOCALAPPDATA"); d != "" && runtime.GOOS == "windows" {
		return filepath.Join(d, "berth", "berth.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("berth-%d.sock", os.Getuid()))
}

// defaultStateDir is the directory defaultStateFile puts its files in:
// $XDG_STATE_HOME/berth, falling back to ~/.local/state/berth. On Windows,
// where no XDG variable is set, it is the per-user state path the service
// logs live under too.
func defaultStateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "berth")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".local", "state", "berth")
	}
	return filepath.Join(os.TempDir(), "berth")
}

// defaultStateFile is $XDG_STATE_HOME/berth/NAME, falling back to
// ~/.local/state/berth/NAME: kept across reboots, unlike the runtime dir.
func defaultStateFile(name string) string {
	return filepath.Join(defaultStateDir(), name)
}

// defaultHarborDB is the SQLite file `berth harbor` persists its history in.
func defaultHarborDB() string { return defaultStateFile("harbor.db") }
