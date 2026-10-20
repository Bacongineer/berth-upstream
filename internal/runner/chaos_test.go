// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

// Scenario 2 from contrib/chaos: an engine that ignores SIGTERM. The drain
// sends SIGTERM to the engine's own process group, waits out the grace, and
// then SIGKILLs that group; a bystander in another group is never touched
// and nothing else is signalled.

import (
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startStubborn starts an engine that swallows SIGTERM across the group:
// the shell traps it, and the sleep it waits in inherits the ignored
// disposition. The returned cmd is ready to drain only after the ready
// line arrives, which the shell prints once the trap is set.
func startStubborn(t *testing.T) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", `trap "" TERM; echo ready; sleep 30`)
	isolate(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() {
		b := make([]byte, 16)
		_, _ = stdout.Read(b)
		close(ready)
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("the stubborn engine never set its trap")
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, waited
}

func TestChaosScenario2IgnoresTerm(t *testing.T) {
	hog, died := startStubborn(t)
	bystander, _ := startSleep(t)

	done := make(chan struct{})
	var logs strings.Builder
	drain(Options{Engine: "hog", Grace: 300 * time.Millisecond}, hog, newLine(nil), done, "test drain", sayTo(&logs))

	select {
	case err := <-died:
		if out := logs.String(); !strings.Contains(out, "ignored terminate for 300ms; killing") {
			t.Fatalf("the kill was not the escalation of an ignored stop; log:\n%s", out)
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("exit error = %v", err)
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("the hog exited on %v, want SIGKILL", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the hog outlived its own drain; log:\n%s", &logs)
	}

	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the bystander did not survive the drain: %v; log:\n%s", err, &logs)
	}
}
