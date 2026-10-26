// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

// Park over the real socket: the runner's own connection asks to park,
// parks, and hears OpGranted{Resume: true} on that same connection when a
// resume lands; the resuming client gets the queue ack or the refusal.

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/node"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// parkCard fakes the card; its processes the test sets directly.
type parkCard struct {
	mu    sync.Mutex
	total uint64
	procs []gpu.Proc
}

func (c *parkCard) set(ps ...gpu.Proc) {
	c.mu.Lock()
	c.procs = ps
	c.mu.Unlock()
}

func (c *parkCard) Read(context.Context) (gpu.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var used uint64
	for _, p := range c.procs {
		used += p.Used
	}
	return gpu.Snapshot{Total: c.total, Used: used, Free: c.total - used,
		Procs: append([]gpu.Proc(nil), c.procs...), At: time.Now()}, nil
}

func parkDaemon(t *testing.T, card gpu.Probe) (string, *broker.Broker) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "berth.sock")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := broker.New(broker.Config{Poll: 20 * time.Millisecond, DefaultWait: 2 * time.Second,
		JuggleWindow: -1, Log: log,
		// Socket-handshake tests own their RAM budget and pressure readings.
		MemAvailable: func() uint64 { return 16 << 30 },
		MemPressure:  func() float64 { return 0 },
	}, card, procs.System{})
	ln, err := server.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// One read before anyone asks: a sized acquire on a card the broker has
	// not read yet sees a zero total and waits.
	b.Tick(ctx)
	go b.Run(ctx)
	go server.Serve(ctx, ln, node.One(b), server.Extras{}, log)
	return sock, b
}

// dial sends one message and reads one reply.
func dial(t *testing.T, sock string, m proto.Msg) proto.Msg {
	t.Helper()
	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(m); err != nil {
		t.Fatal(err)
	}
	r, err := conn.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParkAndResumeOverTheSocket(t *testing.T) {
	card := &parkCard{total: 24 << 30}
	sock, b := parkDaemon(t, card)

	// The runner's connection: acquire, get measured, and be drained by a
	// foreground requester, like the real drain flow.
	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpAcquire, Engine: "vllm", Needs: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	granted, err := conn.Recv()
	if err != nil || granted.Op != proto.OpGranted {
		t.Fatalf("acquire = %v %v", granted, err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpAttach, PID: 101}); err != nil {
		t.Fatal(err)
	}
	card.set(gpu.Proc{PID: 101, Name: "vllm", Used: 8 << 20})
	waitFor(t, "measurement", func() bool {
		return b.Status().Leases[0].Used == 8<<20
	})

	// A foreground waiter that cannot fit beside the measured peak asks
	// for the card: the lease drains, and it stays queued (its connection
	// open) until the park frees the memory.
	waiter, waiterRaw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterRaw.Close()
	if err := waiter.Send(proto.Msg{Op: proto.OpAcquire, Engine: "game", Priority: "foreground"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "draining", func() bool {
		return b.Status().Leases[0].State == "draining"
	})

	// Park: admission, the park call, and the residual leaving the card.
	if err := conn.Send(proto.Msg{Op: proto.OpPark, Lease: granted.Lease}); err != nil {
		t.Fatal(err)
	}
	for {
		if m, err := conn.Recv(); err != nil {
			t.Fatalf("park reply: %v", err)
		} else if m.Op == proto.OpParkOK {
			break
		}
	}
	if err := conn.Send(proto.Msg{Op: proto.OpParked, Lease: granted.Lease}); err != nil {
		t.Fatal(err)
	}
	card.set(gpu.Proc{PID: 101, Name: "vllm", Used: 512 << 20})
	waitFor(t, "parked", func() bool {
		return b.Status().Leases[0].State == "parked"
	})

	// The waiter was granted beside the parked residual, and holds the
	// card until its connection goes.
	waitFor(t, "waiter beside parked", func() bool {
		for _, l := range b.Status().Leases {
			if l.Engine == "game" && l.State == "active" {
				return true
			}
		}
		return false
	})

	// A resume from another client queues the parked engine behind it;
	// the runner hears the grant with Resume set once the waiter's
	// connection ends.
	if r := dial(t, sock, proto.Msg{Op: proto.OpResume, Engine: "vllm"}); r.Op != proto.OpGranted {
		t.Fatalf("resume reply = %+v", r)
	}
	if err := waiterRaw.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		m, err := conn.Recv()
		if err != nil {
			t.Fatalf("runner connection: %v", err)
		}
		if m.Op == proto.OpGranted && m.Resume {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("runner never heard the resume grant; last was %+v", m)
		default:
		}
	}

	// One grant sends one message: an idle lease loop must not resend the
	// grant the way a closed signal channel would.
	spam := make(chan proto.Msg, 4)
	go func() {
		for {
			m, err := conn.Recv()
			if err != nil || m.Op == proto.OpGranted {
				close(spam)
				return
			}
			spam <- m
		}
	}()
	select {
	case m, ok := <-spam:
		if ok {
			t.Fatalf("the resume grant was sent again: %+v", m)
		}
	case <-time.After(400 * time.Millisecond):
	}

	// An engine that is not parked is refused, so the caller starts it
	// the normal way.
	if r := dial(t, sock, proto.Msg{Op: proto.OpResume, Engine: "ghost"}); r.Op != proto.OpRefused ||
		!strings.Contains(r.Reason, "not parked") {
		t.Fatalf("ghost resume = %+v", r)
	}
}
