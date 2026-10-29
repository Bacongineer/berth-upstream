// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package claims

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/procs"
)

// BenchmarkScan reads this machine's real /proc, as the watcher does every
// two seconds.
func BenchmarkScan(b *testing.B) {
	for b.Loop() {
		if _, err := Scan(); err != nil {
			b.Fatal(err)
		}
	}
}

// Scan reports this test process as the kernel does: its name, its parent,
// its command line and its start time.
func TestScanReadsAProcess(t *testing.T) {
	ps, err := Scan()
	if err != nil {
		t.Fatal(err)
	}
	self := os.Getpid()
	for _, p := range ps {
		if p.PID != self {
			continue
		}
		want := Proc{PID: self, Start: procs.System{}.StartTime(self), Name: filepath.Base(os.Args[0])[:min(15, len(filepath.Base(os.Args[0])))],
			Argv0: filepath.Base(os.Args[0]), Cmdline: strings.Join(os.Args, " "), Parent: os.Getppid()}
		if p != want {
			t.Fatalf("scan read\n%+v\nwant\n%+v", p, want)
		}
		return
	}
	t.Fatalf("scan of %d processes did not find this one (%d)", len(ps), self)
}
