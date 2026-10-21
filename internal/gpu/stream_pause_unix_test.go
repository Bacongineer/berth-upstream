// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gpu

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func loops(dir string) []int {
	b, _ := os.ReadFile(filepath.Join(dir, "loops"))
	var pids []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// A paused stream has no nvidia-smi open on the card, so a driver switch can
// run: Pause stops the loop at once and nothing restarts it until Resume.
func TestStreamPauseStopsTheLoop(t *testing.T) {
	bin, dir := fakeSMI(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitUntil(t, func() bool { return len(loops(dir)) == 1 })
	first := loops(dir)[0]

	s.Pause(true)
	waitUntil(t, func() bool { return syscall.Kill(first, 0) != nil })
	time.Sleep(3 * time.Second) // past Run's 2 s restart delay
	if n := len(loops(dir)); n != 1 {
		t.Fatalf("%d nvidia-smi loops started while paused, want 1", n)
	}

	s.Pause(false)
	waitUntil(t, func() bool { return len(loops(dir)) == 2 })
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
