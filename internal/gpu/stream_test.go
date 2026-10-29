// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gpu

import (
	"context"
	"fmt"
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
*-lms*) echo $$ >> $D/loops; while :; do echo "0, Fake 4090, 00000000:01:00.0, 24564, $(cat $D/used), 100"; sleep 0.05; done ;;
*) echo "0, Fake 4090, 00000000:01:00.0, 24564, $(cat $D/used), 100" ;;
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
	c := s.Card(0, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	for i := 0; i < 5; i++ {
		if _, err := c.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls(dir); n != 1 {
		t.Fatalf("process list ran %d times on a steady card, want 1", n)
	}

	put(t, dir, "apps", "00000000:01:00.0, 77, engine, 20000")
	put(t, dir, "used", "20500")
	time.Sleep(200 * time.Millisecond)
	snap, err := c.Read(ctx)
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
	snap, err := s.Card(0, "").Read(context.Background())
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
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	c := s.Card(1, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	snap, err := c.Read(ctx)
	if err != nil || snap.Name != "RTX 6000" || snap.Index != 1 {
		t.Fatalf("before: snap=%+v err=%v", snap, err)
	}

	put(t, dir, "devs", "0, RTX 6000, 00000000:02:00.0, 49140, 300, 48840\n")
	time.Sleep(300 * time.Millisecond)
	snap, err = c.Read(ctx)
	if err != nil || snap.Name != "RTX 6000" || snap.Index != 0 || snap.Used != 300*MiB || len(snap.Devices) != 1 {
		t.Fatalf("after the 4070 left: snap=%+v err=%v", snap, err)
	}

	// The 4070 comes back as gpu0 and the 6000 is gpu1 again.
	put(t, dir, "devs", "0, RTX 4070, 00000000:01:00.0, 12282, 100, 12182\n1, RTX 6000, 00000000:02:00.0, 49140, 300, 48840\n")
	time.Sleep(300 * time.Millisecond)
	if snap, err = c.Read(ctx); err != nil || snap.Name != "RTX 6000" || snap.Index != 1 {
		t.Fatalf("after the 4070 came back: snap=%+v err=%v", snap, err)
	}
}

// When the governed card itself leaves (it is the one in the VM), the read
// fails instead of reporting the card that took its index.
func TestStreamRefusesTheCardThatTookItsIndex(t *testing.T) {
	bin, dir := fakeTwoCards(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	c := s.Card(0, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	if snap, err := c.Read(ctx); err != nil || snap.Name != "RTX 4070" {
		t.Fatalf("before: snap=%+v err=%v", snap, err)
	}

	put(t, dir, "devs", "0, RTX 6000, 00000000:02:00.0, 49140, 300, 48840\n")
	time.Sleep(300 * time.Millisecond)
	if snap, err := c.Read(ctx); err == nil {
		t.Fatalf("read the card that took index 0: snap=%+v", snap)
	}
	cancel()
	time.Sleep(300 * time.Millisecond)
	// A stalled stream falls back to a one-shot query, which must refuse too.
	if snap, err := c.Read(context.Background()); err == nil {
		t.Fatalf("one-shot read the card that took index 0: snap=%+v", snap)
	}
}

// A card named by its address at start is that card from the first read.
// When it starts in a VM, the card left on the host holds index 0 and must
// not be read in its place; when it comes back at index 1 it is read.
func TestStreamNamedByAddressSkipsTheCardAtItsIndex(t *testing.T) {
	bin, dir := fakeTwoCards(t)
	put(t, dir, "devs", "0, RTX 6000, 00000000:01:00.0, 49140, 200, 48940\n")
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond}
	p, err := Open(Options{Backend: "nvidia", Addr: "0000:02:00.0", Stream: s})
	if err != nil {
		t.Fatal(err)
	}
	if snap, err := p.Read(context.Background()); err == nil {
		t.Fatalf("read the card at index 0 while its own was in a VM: snap=%+v", snap)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	put(t, dir, "devs", "0, RTX 6000, 00000000:01:00.0, 49140, 200, 48940\n1, RTX 4070, 00000000:02:00.0, 12282, 100, 12182\n")
	time.Sleep(300 * time.Millisecond)
	snap, err := p.Read(ctx)
	if err != nil || snap.Name != "RTX 4070" || snap.Index != 1 {
		t.Fatalf("after the 4070 came back: snap=%+v err=%v", snap, err)
	}
}

// fakeMachine writes an nvidia-smi stand-in for a two-card box that logs the
// arguments of every loop, one-shot and process list, one line each, in
// $DIR/loops, $DIR/shots and $DIR/lists. Every query prints $DIR/devs, and a
// process list prints $DIR/apps as it was when the list began, after
// sleeping $DIR/slow seconds.
func fakeMachine(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "nvidia-smi")
	script := `#!/bin/sh
D=` + dir + `
case "$*" in
*compute-apps*) echo "$*" >> $D/lists; A=$(cat $D/apps); sleep $(cat $D/slow 2>/dev/null || echo 0); echo "$A" ;;
*-lms*) echo "$*" >> $D/loops; while :; do cat $D/devs; sleep 0.05; done ;;
*) echo "$*" >> $D/shots; cat $D/devs ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "devs", "0, RTX 4090, 00000000:01:00.0, 24564, 9500, 15064\n1, RTX 6000, 00000000:02:00.0, 49140, 31200, 17940\n")
	put(t, dir, "apps", "00000000:01:00.0, 10, llama-server, 9000\n00000000:02:00.0, 20, comfy, worker, 30000\n00000000:02:00.0, 21, ollama, 1000\n")
	return bin, dir
}

func lines(dir, name string) []string {
	b, _ := os.ReadFile(filepath.Join(dir, name))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Every card of the machine reads through one nvidia-smi loop and one
// process list, which is split by the card each process is on.
func TestStreamListsEveryCardInOneQuery(t *testing.T) {
	bin, dir := fakeMachine(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	gpu0, gpu1 := s.Card(0, "0000:01:00.0"), s.Card(1, "0000:02:00.0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	time.Sleep(300 * time.Millisecond)

	var a, b Snapshot
	for i := 0; i < 3; i++ {
		var err error
		if a, err = gpu0.Read(ctx); err != nil {
			t.Fatal(err)
		}
		if b, err = gpu1.Read(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.Name != "RTX 4090" || a.Used != 9500*MiB || fmt.Sprint(a.Procs) != "[{10 llama-server 9437184000 0 0}]" {
		t.Fatalf("gpu0 read %s, %d used, procs %v", a.Name, a.Used/MiB, a.Procs)
	}
	if b.Name != "RTX 6000" || b.Used != 31200*MiB || fmt.Sprint(b.Procs) != "[{20 comfy, worker 31457280000 0 0} {21 ollama 1048576000 0 0}]" {
		t.Fatalf("gpu1 read %s, %d used, procs %v", b.Name, b.Used/MiB, b.Procs)
	}
	if got := lines(dir, "lists"); len(got) != 1 || strings.Contains(got[0], "-i") {
		t.Fatalf("process lists %q, want one for every card", got)
	}
	if got := lines(dir, "loops"); len(got) != 1 || strings.Contains(got[0], "-i") {
		t.Fatalf("loops %q, want one for every card", got)
	}
}

// A card paused for a driver switch is left out of every nvidia-smi berthd
// starts until it resumes, and the machine's other cards go on being read.
// Its processes are listed again when it resumes.
func TestStreamPauseLeavesTheCardOutAndReadsTheOthers(t *testing.T) {
	bin, dir := fakeMachine(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	gpu0, gpu1 := s.Card(0, "0000:01:00.0"), s.Card(1, "0000:02:00.0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitUntil(t, func() bool { _, err := gpu0.Read(ctx); return err == nil })
	before := map[string]int{}
	for _, f := range []string{"loops", "shots", "lists"} {
		before[f] = len(lines(dir, f))
	}

	gpu0.Pause(true)
	waitUntil(t, func() bool { return len(lines(dir, "loops")) == before["loops"]+1 })
	put(t, dir, "devs", "1, RTX 6000, 00000000:02:00.0, 49140, 31300, 17840\n")
	put(t, dir, "apps", "00000000:02:00.0, 20, comfy, worker, 30100\n00000000:02:00.0, 21, ollama, 1000\n")
	waitUntil(t, func() bool { b, err := gpu1.Read(ctx); return err == nil && b.Used == 31300*MiB })
	b, err := gpu1.Read(ctx)
	if err != nil || fmt.Sprint(b.Procs) != "[{20 comfy, worker 31562137600 0 0} {21 ollama 1048576000 0 0}]" {
		t.Fatalf("gpu1 while gpu0 is paused: procs %v, err %v", b.Procs, err)
	}
	for _, f := range []string{"loops", "shots", "lists"} {
		for _, l := range lines(dir, f)[before[f]:] {
			if !strings.HasPrefix(l, "-i 0000:02:00.0 ") {
				t.Fatalf("%s ran %q while gpu0 was paused, want only gpu1 named", f, l)
			}
		}
	}

	put(t, dir, "devs", "0, RTX 4090, 00000000:01:00.0, 24564, 9500, 15064\n1, RTX 6000, 00000000:02:00.0, 49140, 31300, 17840\n")
	put(t, dir, "apps", "00000000:01:00.0, 11, vllm, 9000\n00000000:02:00.0, 20, comfy, worker, 30100\n00000000:02:00.0, 21, ollama, 1000\n")
	gpu0.Pause(false)
	waitUntil(t, func() bool { loops := lines(dir, "loops"); return !strings.Contains(loops[len(loops)-1], "-i") })
	a, err := gpu0.Read(ctx)
	if err != nil || a.Used != 9500*MiB || fmt.Sprint(a.Procs) != "[{11 vllm 9437184000 0 0}]" {
		t.Fatalf("gpu0 after the switch: used %d, procs %v, err %v", a.Used/MiB, a.Procs, err)
	}
}

// A process list that was running while a card was switched does not count
// as current for that card: what it saw was from before the switch.
func TestStreamListAcrossASwitchIsListedAgain(t *testing.T) {
	bin, dir := fakeMachine(t)
	s := &Stream{Bin: bin, Interval: 50 * time.Millisecond, AppsEvery: time.Hour}
	gpu0, gpu1 := s.Card(0, "0000:01:00.0"), s.Card(1, "0000:02:00.0")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	waitUntil(t, func() bool { _, err := gpu0.Read(ctx); return err == nil })

	put(t, dir, "slow", "0.5")
	listed := len(lines(dir, "lists"))
	put(t, dir, "devs", "0, RTX 4090, 00000000:01:00.0, 24564, 9500, 15064\n1, RTX 6000, 00000000:02:00.0, 49140, 31300, 17840\n")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if b, err := gpu1.Read(ctx); err == nil && b.Used == 31300*MiB || ctx.Err() != nil {
				return
			}
		}
	}()
	waitUntil(t, func() bool { return len(lines(dir, "lists")) > listed }) // gpu1's memory moved: a list is running
	gpu0.Pause(true)
	put(t, dir, "apps", "00000000:01:00.0, 11, vllm, 9000\n00000000:02:00.0, 20, comfy, worker, 30100\n")
	gpu0.Pause(false)
	<-done
	put(t, dir, "slow", "0")

	waitUntil(t, func() bool { _, err := gpu0.Read(ctx); return err == nil })
	a, err := gpu0.Read(ctx)
	if err != nil || fmt.Sprint(a.Procs) != "[{11 vllm 9437184000 0 0}]" {
		t.Fatalf("gpu0 after a switch during a list: procs %v, err %v", a.Procs, err)
	}
}
