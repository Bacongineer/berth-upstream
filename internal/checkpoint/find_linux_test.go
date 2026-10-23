// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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
