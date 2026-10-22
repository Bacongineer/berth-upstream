// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gpu

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSMI writes an nvidia-smi stand-in: loop mode prints the memory line
// from $DIR/used every 50 ms; a compute-apps query prints $DIR/apps and
// counts itself in $DIR/calls.
func fakeSMI(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "nvidia-smi")
	script := `#!/bin/sh
D=` + dir + `
case "$*" in
*compute-apps*) echo x >> $D/calls; cat $D/apps; exit 0 ;;
*-lms*) echo $$ >> $D/loops; while :; do echo "0, Fake 4090, 24564, $(cat $D/used), 100"; sleep 0.05; done ;;
*) echo "0, Fake 4090, 24564, $(cat $D/used), 100" ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "used", "500")
	put(t, dir, "apps", "")
	return bin, dir
}

func put(t *testing.T, dir, name, v string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o644); err != nil {
		t.Fatal(err)
	}
}

func calls(dir string) int {
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return strings.Count(string(b), "x")
}

func TestStreamListsProcessesOnlyWhenMemoryMoves(t *testing.T) {
	bin, dir := fakeSMI(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	for i := 0; i < 5; i++ {
		if _, err := s.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls(dir); n != 1 {
		t.Fatalf("process list ran %d times on a steady card, want 1", n)
	}

	put(t, dir, "apps", "77, engine, 20000")
	put(t, dir, "used", "20500")
	time.Sleep(200 * time.Millisecond)
	snap, err := s.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls(dir) != 2 || len(snap.Procs) != 1 || snap.Procs[0].PID != 77 || snap.Used != 20500*MiB {
		t.Fatalf("after a load: calls=%d snap=%+v", calls(dir), snap)
	}
}

func TestStreamFallsBackWhenStalled(t *testing.T) {
	bin, dir := fakeSMI(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond}
	// Never started: no stream, so every Read is a one-shot query.
	snap, err := s.Read(context.Background())
	if err != nil || snap.Total != 24564*MiB || len(snap.Devices) != 1 {
		t.Fatalf("snap=%+v err=%v", snap, err)
	}
	if calls(dir) != 1 {
		t.Fatalf("one-shot fallback did not list processes")
	}
}

// fakeTwoCards writes an nvidia-smi stand-in for a two-card box: every query
// prints $DIR/devs (one line per card, with its bus id), and a compute-apps
// query records its arguments in $DIR/args.
func fakeTwoCards(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "nvidia-smi")
	script := `#!/bin/sh
D=` + dir + `
case "$*" in
*compute-apps*) echo "$*" > $D/args; exit 0 ;;
*-lms*) while :; do cat $D/devs; sleep 0.05; done ;;
*) cat $D/devs ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "devs", "0, RTX 4070, 00000000:01:00.0, 12282, 100, 12182\n1, RTX 6000, 00000000:02:00.0, 49140, 200, 48940\n")
	return bin, dir
}

// A card passed through to a VM leaves nvidia-smi and the cards after it are
// renumbered. The daemon for gpu1 must keep reading the RTX 6000 at its new
// index 0, and the RTX 4070 must never be read as gpu1's card.
func TestStreamFollowsItsCardWhenIndicesShift(t *testing.T) {
	bin, dir := fakeTwoCards(t)
	s := &Stream{Index: 1, Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	snap, err := s.Read(ctx)
	if err != nil || snap.Name != "RTX 6000" || snap.Index != 1 {
		t.Fatalf("before: snap=%+v err=%v", snap, err)
	}

	put(t, dir, "devs", "0, RTX 6000, 00000000:02:00.0, 49140, 300, 48840\n")
	time.Sleep(300 * time.Millisecond)
	snap, err = s.Read(ctx)
	if err != nil || snap.Name != "RTX 6000" || snap.Index != 0 || snap.Used != 300*MiB || len(snap.Devices) != 1 {
		t.Fatalf("after the 4070 left: snap=%+v err=%v", snap, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if !strings.Contains(string(args), "-i 00000000:02:00.0") {
		t.Fatalf("process list asked for %q, want the card's bus id", args)
	}

	// The 4070 comes back as gpu0 and the 6000 is gpu1 again.
	put(t, dir, "devs", "0, RTX 4070, 00000000:01:00.0, 12282, 100, 12182\n1, RTX 6000, 00000000:02:00.0, 49140, 300, 48840\n")
	time.Sleep(300 * time.Millisecond)
	if snap, err = s.Read(ctx); err != nil || snap.Name != "RTX 6000" || snap.Index != 1 {
		t.Fatalf("after the 4070 came back: snap=%+v err=%v", snap, err)
	}
}

// When the governed card itself leaves (it is the one in the VM), the read
// fails instead of reporting the card that took its index.
func TestStreamRefusesTheCardThatTookItsIndex(t *testing.T) {
	bin, dir := fakeTwoCards(t)
	s := &Stream{Index: 0, Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	if snap, err := s.Read(ctx); err != nil || snap.Name != "RTX 4070" {
		t.Fatalf("before: snap=%+v err=%v", snap, err)
	}

	put(t, dir, "devs", "0, RTX 6000, 00000000:02:00.0, 49140, 300, 48840\n")
	time.Sleep(300 * time.Millisecond)
	if snap, err := s.Read(ctx); err == nil {
		t.Fatalf("read the card that took index 0: snap=%+v", snap)
	}
	cancel()
	time.Sleep(300 * time.Millisecond)
	// A stalled stream falls back to a one-shot query, which must refuse too.
	if snap, err := s.Read(context.Background()); err == nil {
		t.Fatalf("one-shot read the card that took index 0: snap=%+v", snap)
	}
}
