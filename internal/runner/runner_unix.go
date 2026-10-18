//go:build unix

package runner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

var stopSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// isolate puts the engine in its own process group, so a drain reaches the
// processes it forks (Strata's server spawns the engine that holds the memory).
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// afterStart has nothing to do: the process group was set at start.
func afterStart(*exec.Cmd) {}

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
