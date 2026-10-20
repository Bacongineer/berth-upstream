// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

// Reclaim over the real socket: a listing names what no lease recorded,
// a confirm run stops exactly what a lease recorded, re-checking identity
// first, and strangers are report only.

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// listedCard fakes a card whose processes the test sets directly.
type listedCard struct {
	mu    sync.Mutex
	total uint64
	procs []gpu.Proc
}

func (c *listedCard) set(ps ...gpu.Proc) {
	c.mu.Lock()
	c.procs = ps
	c.mu.Unlock()
}

func (c *listedCard) Read(context.Context) (gpu.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var used uint64
	for _, p := range c.procs {
		used += p.Used
	}
	return gpu.Snapshot{Total: c.total, Used: used, Free: c.total - used,
		Procs: append([]gpu.Proc(nil), c.procs...), At: time.Now()}, nil
}

// startSleep runs a real child and keeps it out of the listings with a
// kill on cleanup. Wait returns through cmd.Wait; callers that need the
// death reaped do it themselves.
func startSleep(t *testing.T, secs string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", secs)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func reap(cmd *exec.Cmd) chan struct{} {
	ch := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(ch)
	}()
	return ch
}

func TestReclaimStopsOnlyRecordedHolders(t *testing.T) {
	card := &listedCard{total: 24 << 30}
	sock := filepath.Join(t.TempDir(), "berth.sock")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := broker.New(broker.Config{Poll: 20 * time.Millisecond, DefaultWait: 5 * time.Second,
		JuggleWindow: -1, Log: log}, card, procs.System{})
	ln, err := server.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go b.Run(ctx)
	go server.Serve(ctx, ln, b, server.Extras{}, log)

	engine := startSleep(t, "60")
	stranger := startSleep(t, "60")
	doomed := startSleep(t, "60") // recorded, then gone from /proc before the confirm run

	leaseA, err := b.Acquire(ctx, broker.Request{Engine: "a", Needs: 8 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	leaseA.Attach(engine.Process.Pid)
	leaseM, err := b.Acquire(ctx, broker.Request{Engine: "m", Needs: 2 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	leaseM.Attach(doomed.Process.Pid)
	card.set(
		gpu.Proc{PID: engine.Process.Pid, Name: "sleep", Used: 8 << 20},
		gpu.Proc{PID: stranger.Process.Pid, Name: "sleep", Used: 1 << 20},
		gpu.Proc{PID: doomed.Process.Pid, Name: "sleep", Used: 2 << 20},
	)
	waitFor(t, "attribution", func() bool {
		st := b.Status()
		pids := func(ls []proto.Lease) []int {
			var out []int
			for _, l := range ls {
				out = append(out, l.GPUPIDs...)
			}
			return out
		}
		return slices.Contains(pids(st.Leases), engine.Process.Pid) && slices.Contains(pids(st.Leases), doomed.Process.Pid)
	})
	leaseA.Release()
	leaseM.Release()
	waitFor(t, "leases releasing", func() bool {
		st := b.Status()
		for _, l := range st.Leases {
			if l.State != "releasing" {
				return false
			}
		}
		return len(st.Leases) == 2
	})

	// The doomed one exits and is reaped; the fake card keeps reporting its PID.
	if err := doomed.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-reap(doomed)

	// Listing only: the three holders are named, none touched.
	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpReclaim}); err != nil {
		t.Fatal(err)
	}
	m, err := conn.Recv()
	if err != nil || m.Op != proto.OpReclaim {
		t.Fatalf("listing reply = %v %v", m, err)
	}
	raw.Close()
	recorded := map[int]proto.Holder{}
	for _, h := range m.Holders {
		switch h.PID {
		case engine.Process.Pid, doomed.Process.Pid:
			if h.Lease == "" || h.Stopped || h.Skipped != "" {
				t.Fatalf("recorded holder = %+v", h)
			}
			recorded[h.PID] = h
		case stranger.Process.Pid:
			if h.Lease != "" || h.Used != 1<<20 {
				t.Fatalf("stranger = %+v", h)
			}
		default:
			t.Fatalf("unknown holder = %+v", h)
		}
	}
	if len(recorded) != 2 {
		t.Fatalf("recorded = %+v", recorded)
	}

	// Confirm: SIGTERM to the engine child and confirmed gone; the doomed
	// PID no longer runs as recorded, so it is never signalled; the
	// stranger is never signalled.
	conn, raw, err = server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	engineGone := reap(engine)
	if err := conn.Send(proto.Msg{Op: proto.OpReclaim, Confirm: true, Grace: 2}); err != nil {
		t.Fatal(err)
	}
	m, err = conn.Recv()
	if err != nil || m.Op != proto.OpReclaim {
		t.Fatalf("confirm reply = %v %v", m, err)
	}
	raw.Close()
	for _, h := range m.Holders {
		switch h.PID {
		case engine.Process.Pid:
			if !h.Stopped {
				t.Fatalf("engine holder = %+v, want stopped", h)
			}
		case doomed.Process.Pid:
			if h.Lease == "" || h.Skipped == "" {
				t.Fatalf("doomed holder = %+v, want a skip reason", h)
			}
		case stranger.Process.Pid:
			if h.Skipped != "" || h.Stopped {
				t.Fatalf("stranger = %+v, want it untouched", h)
			}
		}
	}
	<-engineGone
	if stranger.ProcessState != nil {
		t.Fatal("the stranger was signalled")
	}
}
