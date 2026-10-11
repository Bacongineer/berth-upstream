//go:build windows

package runner

import (
	"errors"
	"os"
	"os/exec"
)

// Windows is v1. Until then the engine is stopped directly and
// its children are not reached.
var stopSignals = []os.Signal{os.Interrupt}

func isolate(*exec.Cmd)                      {}
func signalGroup(cmd *exec.Cmd, _ os.Signal) { _ = cmd.Process.Kill() }
func terminate(cmd *exec.Cmd)                { _ = cmd.Process.Kill() }
func kill(cmd *exec.Cmd)                     { _ = cmd.Process.Kill() }

func exitCode(_ *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
