// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package checkpoint

import (
	"context"
	"errors"
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
