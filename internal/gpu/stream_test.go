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
*-lms*) while :; do echo "0, Fake 4090, 24564, $(cat $D/used), 100"; sleep 0.05; done ;;
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
