// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

import (
	"testing"
	"time"
)

// Five cards ticking together read the process table once.
func TestSharedReadsOncePerAge(t *testing.T) {
	reads := 0
	list := func() []Proc { reads++; return []Proc{{PID: reads}} }
	table := Shared(list, time.Hour)
	for range 5 {
		if got := table(); len(got) != 1 || got[0].PID != 1 {
			t.Fatalf("table = %+v, want the first read", got)
		}
	}
	if reads != 1 {
		t.Fatalf("%d reads for five cards, want 1", reads)
	}

	fresh := Shared(list, 0)
	fresh()
	fresh()
	if reads != 3 {
		t.Fatalf("%d reads with no max age, want 3", reads)
	}
}
