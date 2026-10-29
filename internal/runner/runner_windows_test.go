// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package runner

import (
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestStopSignalsIncludeConsoleClose(t *testing.T) {
	if !slices.Contains(stopSignals, os.Signal(syscall.SIGTERM)) {
		t.Fatalf("stopSignals = %v, want SIGTERM (console close, logoff, shutdown)", stopSignals)
	}
}

func TestJobDroppedWhenEngineExits(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "exit", "0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	afterStart(cmd)
	pid := cmd.Process.Pid
	if _, ok := jobs.Load(pid); !ok {
		t.Fatal("no job recorded after start")
	}
	_ = cmd.Wait()
	afterWait(cmd)
	if _, ok := jobs.Load(pid); ok {
		t.Fatal("job entry still there after the engine exited")
	}
}

func TestKillFallsBackWhenJobTerminateFails(t *testing.T) {
	cmd := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// A stale entry: the handle is not a job, as after pid reuse.
	jobs.Store(cmd.Process.Pid, windows.Handle(0))
	kill(cmd)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process survived kill with a stale job entry")
	}
}
