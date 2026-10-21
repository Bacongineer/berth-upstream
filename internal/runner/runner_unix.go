// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/sysd"
)

var stopSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// isolate puts the engine in its own process group, so a drain reaches the
// processes it forks (Strata's server spawns the engine that holds the memory).
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// afterStart has nothing to do: the process group was set at start.
func afterStart(*exec.Cmd) {}

// scopeToChild starts the engine in its own transient scope, so the
// lease can name every process under it by cgroup even when one escapes
// the process tree (chaos scenario 3: a child that forks and whose
// parent exits before any tick records it). A run the gateway started
// already lives in a berth-engine scope and passes that scope down;
// without a user manager the child runs in this cgroup, as always.
func scopeToChild(as string, argv []string, log *slog.Logger) []string {
	// Tests start fake engines: never in the developer's own user manager.
	if testing.Testing() {
		return argv
	}
	if _, scoped := procs.Scope(os.Getpid()); scoped {
		return argv
	}
	if !sysd.ScopeReady() {
		if log != nil {
			log.Debug("no systemd user manager; the engine runs in this run's cgroup", "engine", as)
		}
		return argv
	}
	return sysd.ScopeArgv("berth-run", as, argv)
}

func signalGroup(cmd *exec.Cmd, s os.Signal) {
	if sig, ok := s.(syscall.Signal); ok {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

func terminate(cmd *exec.Cmd) { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
func kill(cmd *exec.Cmd)      { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }

func exitCode(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 1
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ee.ExitCode()
}
