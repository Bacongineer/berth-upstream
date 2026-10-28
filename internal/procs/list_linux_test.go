// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

import (
	"os"
	"testing"
)

// List finds this test process with its real parent and start time.
func TestListFindsThisProcess(t *testing.T) {
	me := os.Getpid()
	for _, p := range List() {
		if p.PID == me {
			if p.Parent != os.Getppid() || p.Start != (System{}).StartTime(me) || p.Name == "" {
				t.Fatalf("this process read as %+v, want parent %d and start %d", p, os.Getppid(), (System{}).StartTime(me))
			}
			return
		}
	}
	t.Fatal("List did not include this process")
}

func BenchmarkList(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		List()
	}
}
