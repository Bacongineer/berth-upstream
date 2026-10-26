// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
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
