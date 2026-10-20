// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

import (
	"os"
	"testing"
)

func TestSystemSelf(t *testing.T) {
	var s System
	if s.Parent(os.Getpid()) != os.Getppid() {
		t.Fatalf("parent = %d, want %d", s.Parent(os.Getpid()), os.Getppid())
	}
	if s.StartTime(os.Getpid()) == 0 {
		t.Fatal("no start time for self")
	}
	if s.StartTime(1<<30) != 0 || s.Parent(1<<30) != 0 {
		t.Fatal("a missing process must read as 0")
	}
}
