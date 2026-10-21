// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"os/exec"
	"syscall"
)

// detach moves the run process to its own session, so a Ctrl-C meant for
// the gateway's process group does not reach it. A service stop still
// can: systemd kills by cgroup, which only the scope the run leaves
// (sysd.ScopeArgv) survives.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
