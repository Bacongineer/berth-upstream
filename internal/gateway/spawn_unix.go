// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// detach moves the run process to its own session, so a Ctrl-C meant for
// the gateway's process group does not reach it. A service stop still
// can: systemd kills by cgroup, which only the scope below leaves.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// scopeArgv runs the command through the user manager's scope when one is
// reachable, so the engine leaves the gateway's cgroup and survives a
// gateway restart, stop or upgrade: --collect drops the scope when the
// run ends, --quiet keeps systemd-run's own notice out of the output.
func scopeArgv(name string, argv []string) []string {
	// Tests start fake engines: never in the developer's own user manager.
	if testing.Testing() || !userScopeReady() {
		return argv
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	unit := fmt.Sprintf("berth-engine-%s-%x", unitName(name), rnd)
	return append([]string{"systemd-run", "--user", "--scope", "--unit", unit, "--collect", "--quiet", "--"}, argv...)
}

// userScopeReady reports whether a systemd user manager can take a scope:
// systemd-run on PATH, and the manager's private socket in the runtime
// directory.
func userScopeReady() bool {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, "systemd", "private"))
	return err == nil
}
