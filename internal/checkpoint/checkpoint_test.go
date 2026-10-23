// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeTool records each call and fails the action named in fail.
func fakeTool(fail string) (Tool, *[]string) {
	var calls []string
	return func(_ context.Context, argv ...string) (string, error) {
		line := strings.Join(argv, " ")
		calls = append(calls, line)
		if fail != "" && strings.Contains(line, "--action "+fail) {
			return "CUDA_ERROR_NOT_SUPPORTED", errors.New("exit status 1")
		}
		return "", nil
	}, &calls
}

func TestSuspendLocksThenCheckpoints(t *testing.T) {
	run, calls := fakeTool("")
	if err := Suspend(context.Background(), run, 4242, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	want := "--action lock --pid 4242 --timeout 30000|--action checkpoint --pid 4242"
	if got := strings.Join(*calls, "|"); got != want {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// A checkpoint that fails leaves the process unlocked, never stuck.
func TestSuspendUnlocksWhenTheCheckpointFails(t *testing.T) {
	run, calls := fakeTool("checkpoint")
	err := Suspend(context.Background(), run, 7, time.Second)
	if err == nil || err.Error() != "checkpoint pid 7: exit status 1: CUDA_ERROR_NOT_SUPPORTED" {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(*calls, "|"); got != "--action lock --pid 7 --timeout 1000|--action checkpoint --pid 7|--action unlock --pid 7" {
		t.Fatalf("calls = %q", got)
	}
}

func TestRestoreThenUnlocks(t *testing.T) {
	run, calls := fakeTool("")
	if err := Restore(context.Background(), run, 4242); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, "|"); got != "--action restore --pid 4242|--action unlock --pid 4242" {
		t.Fatalf("calls = %q", got)
	}
}

// A service whose PATH lacks the user's bin directory still finds the tool
// installed beside the berth binary.
func TestFindBesideTheBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Find(); err == nil || err.Error() != "cuda-checkpoint is not on PATH or beside the berth binary (github.com/NVIDIA/cuda-checkpoint, driver 550 or later)" {
		t.Fatalf("Find with no tool = %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	beside := filepath.Join(filepath.Dir(exe), "cuda-checkpoint")
	if err := os.WriteFile(beside, []byte("#!/bin/sh\necho beside \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(beside) })
	run, err := Find()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(context.Background(), "--get-state", "--pid", "1"); err != nil || out != "beside --get-state --pid 1" {
		t.Fatalf("ran %q %v", out, err)
	}
}
