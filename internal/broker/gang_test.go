// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/eventlog"
	"github.com/T-Crypt/berth/internal/procs"
)

// node is cards on one machine, sharing what a node shares.
func node(t *testing.T, names ...string) []*Broker {
	t.Helper()
	events, ids, seq := eventlog.New(), &atomic.Uint64{}, &atomic.Uint64{}
	var bs []*Broker
	for _, name := range names {
		b := New(Config{Card: name, Events: events, IDs: ids, Seq: seq, ReleaseGrace: time.Hour,
			DefaultWait: 5 * time.Second, JuggleWindow: -1, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
			&card{total: 24 * GiB}, procs.Fake{Starts: map[int]uint64{100: 7}})
		b.Tick(context.Background())
		bs = append(bs, b)
	}
	return bs
}

type gangResult struct {
	ls  []*Lease
	err error
}

func acquireAll(cards []*Broker, r Request) chan gangResult {
	ch := make(chan gangResult, 1)
	go func() {
		ls, err := AcquireAll(context.Background(), cards, r, nil)
		ch <- gangResult{ls, err}
	}()
	return ch
}

func tickAll(bs []*Broker) {
	for _, b := range bs {
		b.Tick(context.Background())
	}
}

func holds(b *Broker, engine string) bool {
	for _, l := range b.Status().Leases {
		if l.Engine == engine {
			return true
		}
	}
	return false
}

// A lease on two cards is granted on both at once or on neither. While gpu1
// is held, maya holds nothing on gpu0, and gets both cards once gpu1 frees.
func TestSeveralCardsGrantTogether(t *testing.T) {
	bs := node(t, "gpu0", "gpu1")
	holder := must(t, acquire(bs[1], Request{Engine: "voice", Priority: Foreground}))

	ch := acquireAll(bs, Request{Engine: "maya", Priority: Batch})
	time.Sleep(100 * time.Millisecond)
	tickAll(bs)
	select {
	case r := <-ch:
		t.Fatalf("granted while gpu1 is held: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	if holds(bs[0], "maya") {
		t.Fatal("maya holds gpu0 while it waits for gpu1")
	}

	holder.Release()
	tickAll(bs)
	var r gangResult
	select {
	case r = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("not granted after gpu1 freed")
	}
	if r.err != nil || len(r.ls) != 2 || r.ls[0].ID() != r.ls[1].ID() {
		t.Fatalf("grant = %+v, want one lease ID on both cards", r)
	}
	if !holds(bs[0], "maya") || !holds(bs[1], "maya") {
		t.Fatal("maya is not on both cards")
	}
}

// Two leases that each want both cards, asked in opposite card order at the
// same moment, both finish: one runs, releases, then the other runs.
func TestTwoGangsInOppositeCardOrderBothFinish(t *testing.T) {
	t.Cleanup(func() { shareQueued = nil })
	for i := 0; i < 5; i++ {
		bs := node(t, "gpu0", "gpu1")
		// x queues on gpu0, then waits while y queues on both cards, so y
		// reaches gpu1 before x does.
		yQueued := make(chan struct{})
		shareQueued = func(engine, card string) {
			switch {
			case engine == "x" && card == "gpu0":
				<-yQueued
			case engine == "y" && card == "gpu1":
				close(yQueued)
			}
		}
		x := acquireAll([]*Broker{bs[0], bs[1]}, Request{Engine: "x", Priority: Batch})
		time.Sleep(5 * time.Millisecond)
		y := acquireAll([]*Broker{bs[1], bs[0]}, Request{Engine: "y", Priority: Batch})
		var first gangResult
		var second chan gangResult
		select {
		case first = <-x:
			second = y
		case first = <-y:
			second = x
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: neither gang was granted", i)
		}
		if first.err != nil {
			t.Fatalf("round %d: %v", i, first.err)
		}
		for _, l := range first.ls {
			l.Release()
		}
		tickAll(bs)
		select {
		case r := <-second:
			if r.err != nil || len(r.ls) != 2 {
				t.Fatalf("round %d: second gang = %+v", i, r)
			}
			for _, l := range r.ls {
				l.Release()
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: the second gang never got both cards", i)
		}
	}
}

// An engine that held a lease on two cards comes back on both under one
// lease ID after a berthd restart, or on neither.
func TestReattachAllIsOneLeaseOnEveryCard(t *testing.T) {
	cards := node(t, "gpu0", "gpu1")
	ls, err := ReattachAll(cards, Request{Engine: "maya"}, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].ID() != ls[1].ID() {
		t.Fatalf("shares %v, want one lease ID on both cards", ls)
	}
	for _, b := range cards {
		if !holds(b, "maya") {
			t.Errorf("%s does not hold maya", b.cfg.Card)
		}
	}

	cards = node(t, "gpu0", "gpu1")
	if _, err := cards[1].Reattach(Request{Engine: "voice"}, 100, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := ReattachAll(cards, Request{Engine: "maya"}, 100, 7); err == nil {
		t.Fatal("pid 100 already holds a lease on gpu1, and the re-attach was granted")
	}
	tickAll(cards)
	if holds(cards[0], "maya") {
		t.Error("gpu0 kept its share of a re-attach gpu1 refused")
	}
}
