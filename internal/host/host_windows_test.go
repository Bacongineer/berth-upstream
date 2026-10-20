// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package host

import "testing"

// A real Windows machine must read its own memory: the totals are the
// installed RAM, the available figure sits inside it, and the page file
// is the commit limit. Nothing is asserted against a value the system
// could change between calls beyond these invariants.
func TestStatsWindows(t *testing.T) {
	if got := MemAvailable(""); got == 0 {
		t.Fatal("MemAvailable = 0, want GlobalMemoryStatusEx's reading")
	}
	s := (&Reader{}).Read()
	if !s.Supported {
		t.Fatal("Read reports unsupported on Windows")
	}
	if s.RAMTotal == 0 {
		t.Fatal("RAMTotal = 0, want the installed RAM")
	}
	if s.RAMUsed > s.RAMTotal {
		t.Fatalf("RAMUsed %d over RAMTotal %d", s.RAMUsed, s.RAMTotal)
	}
	if s.SwapTotal == 0 {
		t.Fatal("SwapTotal = 0, want the commit limit")
	}
	if s.SwapUsed > s.SwapTotal {
		t.Fatalf("SwapUsed %d over SwapTotal %d", s.SwapUsed, s.SwapTotal)
	}
	if s.Cores <= 0 {
		t.Fatalf("Cores = %d, want the real count", s.Cores)
	}
}
