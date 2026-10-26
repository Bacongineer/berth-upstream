// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"os"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// The lend ask a runner reads off the socket carries Needs in bytes, as
// proto documents it, so the engine is asked for the waiter's shortfall.
func TestLendAskOverTheSocketIsInBytes(t *testing.T) {
	card := &parkCard{total: 24 << 30}
	sock, b := parkDaemon(t, card)

	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpAcquire, Engine: "strata", Lend: true}); err != nil {
		t.Fatal(err)
	}
	if g, err := conn.Recv(); err != nil || g.Op != proto.OpGranted {
		t.Fatalf("acquire = %v %v", g, err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpAttach, PID: 101}); err != nil {
		t.Fatal(err)
	}
	card.set(gpu.Proc{PID: 101, Name: "strata", Used: 22 << 30})
	waitFor(t, "measurement", func() bool { return b.Status().Leases[0].Used == 22<<30 })

	waiter, waiterRaw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterRaw.Close()
	if err := waiter.Send(proto.Msg{Op: proto.OpAcquire, Engine: "ninfer", Priority: "foreground", Needs: 4 << 30}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, err := conn.Recv()
		if err != nil {
			t.Fatalf("holder connection: %v", err)
		}
		if m.Op != proto.OpLend {
			continue
		}
		if m.Needs != 4<<30 || m.Free != 2048 {
			t.Fatalf("lend ask = needs %d bytes, free %d MiB; want %d bytes, 2048 MiB", m.Needs, m.Free, uint64(4<<30))
		}
		return
	}
	t.Fatal("the holder was never asked to lend")
}

// The lend fields a re-attach carries arrive at the broker in bytes, as proto
// documents them: Needs stays the lease's budget, and Lent is what the engine
// gave with the ticket. The holder then counts its budget less the lend, so a
// waiter for the space still beside it is granted; on the MiB reading (the bug
// #205's socket tests exist for) the same waiter finds no room and waits.
func TestReattachWithALendArrivesInBytes(t *testing.T) {
	card := &parkCard{total: 24 << 30}
	sock, b := parkDaemon(t, card)

	// A borrower stands on the lent space from the start, so the take-back
	// stays parked on "the card has no room" and the holder keeps counting
	// its budget less the lend while the waiter is considered.
	ninfer, ninferRaw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ninferRaw.Close()
	if err := ninfer.Send(proto.Msg{Op: proto.OpAcquire, Engine: "ninfer", Priority: "foreground", Needs: 8 << 30}); err != nil {
		t.Fatal(err)
	}
	if g, err := ninfer.Recv(); err != nil || g.Op != proto.OpGranted {
		t.Fatalf("ninfer acquire = %v %v", g, err)
	}
	if err := ninfer.Send(proto.Msg{Op: proto.OpAttach, PID: 111}); err != nil {
		t.Fatal(err)
	}
	card.set(gpu.Proc{PID: 111, Name: "ninfer", Used: 8 << 30})
	waitFor(t, "the borrower's measurement", func() bool {
		for _, l := range b.Status().Leases {
			if l.Engine == "ninfer" {
				return l.Used == 8<<30
			}
		}
		return false
	})

	// The identity a re-attach vouches for: this test process, with its
	// real start time, standing in for the engine that outlived the daemon.
	pid := os.Getpid()
	start := procs.System{}.StartTime(pid)
	if start == 0 {
		t.Fatalf("no start time for pid %d", pid)
	}
	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// A 20 GiB budget with an open 8 GiB lend: the engine's answer said
	// 8192 MiB, and the wire carries bytes.
	if err := conn.Send(proto.Msg{Op: proto.OpReattach, Engine: "maya", Needs: 20 << 30,
		PID: pid, PIDStart: start, Lend: true, LendID: "17", Lent: 8 << 30}); err != nil {
		t.Fatal(err)
	}
	if g, err := conn.Recv(); err != nil || g.Op != proto.OpGranted {
		t.Fatalf("reattach = %v %v", g, err)
	}
	card.set(gpu.Proc{PID: 111, Name: "ninfer", Used: 8 << 30}, gpu.Proc{PID: pid, Name: "maya", Used: 12 << 30})
	waitFor(t, "the holder's measurement", func() bool {
		for _, l := range b.Status().Leases {
			if l.Engine == "maya" {
				return l.Used == 12<<30
			}
		}
		return false
	})

	// A waiter for 4 GiB fits only against the reduced budget: 24 less the
	// borrower's 8 less maya's 20-is-now-12 is exactly 4, where the full
	// budget or an 8 MiB (mis-scaled) lend leaves it none.
	waiter, waiterRaw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterRaw.Close()
	if err := waiter.Send(proto.Msg{Op: proto.OpAcquire, Engine: "voice", Priority: "foreground", Needs: 4 << 30}); err != nil {
		t.Fatal(err)
	}
	m, err := waiter.Recv()
	if err != nil {
		t.Fatalf("waiter: %v", err)
	}
	if m.Op != proto.OpGranted {
		t.Fatalf("the waiter got op=%s (%s), want granted: the 8 GiB lend must count in bytes", m.Op, m.Reason)
	}
}
