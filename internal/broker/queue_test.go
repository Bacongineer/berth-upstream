// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/swapcost"
)

// wait acquires an engine and returns the first wait reason the broker
// sends it. That reason is sent from Acquire's own schedule pass, so
// blocking on it also leaves the next wait in the file queued behind this
// one, in the broker's arrival order.
func wait(t *testing.T, b *Broker, engine string, prio Priority) string {
	t.Helper()
	out := make(chan string, 16)
	go func() {
		_, _ = b.Acquire(context.Background(), Request{Engine: engine, Priority: prio}, func(s string) {
			out <- s
		})
	}()
	select {
	case s := <-out:
		return s
	case <-time.After(2 * time.Second):
		t.Fatalf("%s heard no wait reason", engine)
	}
	return ""
}

// The queue orders by priority, then the broker's arrival count, and every
// reason a waiter is sent starts with its place: the head reads "next in
// line", each seat behind counts the waiters ahead and names the one
// directly ahead, by priority and engine.
func TestWaitReasonsCarryTheQueuePlace(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, _ := setup(t, tree)

	holder := must(t, acquire(b, Request{Engine: "ninfer", Priority: Batch}))
	holder.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "ninfer-serve", Used: 22 * GiB})
	b.Tick(context.Background())

	if got := wait(t, b, "alpha", Interactive); got != "next in line: draining ninfer [batch, active]" {
		t.Fatalf("head reason = %q, want the place and the drain", got)
	}
	const second = "2nd in line (behind interactive alpha): waiting for ninfer [batch, draining] to free memory"
	if got := wait(t, b, "beta", Interactive); got != second {
		t.Fatalf("second reason = %q, want %q", got, second)
	}
	const third = "3rd in line (behind interactive beta): waiting for ninfer [batch, draining] to free memory"
	if got := wait(t, b, "gamma", Batch); got != third {
		t.Fatalf("third reason = %q, want %q", got, third)
	}
}

// A single waiter has nobody ahead: it is next in line, full stop. Here the
// holder outranks it, so the card stays held and nobody is drained.
func TestWaitReasonSaysNextInLineAlone(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, _ := setup(t, tree)

	holder := must(t, acquire(b, Request{Engine: "ninfer", Priority: Foreground}))
	holder.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "ninfer-serve", Used: 22 * GiB})
	b.Tick(context.Background())

	if got := wait(t, b, "strata", Interactive); got != "next in line: held by ninfer [foreground, active]" {
		t.Fatalf("reason = %q, want the place and the hold", got)
	}
}

// With a swap book the reasons end with the start estimate: the measured
// starts in the waiter's chain, one per seat ahead plus its own, as one
// number; an unmeasured start adds no number and says "unmeasured"; with
// no book at all the reason carries the place only.
func TestWaitReasonsCarryTheMeasuredStart(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, _ := setup(t, tree)

	book, err := swapcost.NewBook("")
	if err != nil {
		t.Fatal(err)
	}
	book.Record(swapcost.Sample{Engine: "alpha", Tier: swapcost.Cold, Took: 30 * time.Second})
	book.Record(swapcost.Sample{Engine: "alpha", Tier: swapcost.Cold, Took: 40 * time.Second})
	book.Record(swapcost.Sample{Engine: "beta", Tier: swapcost.Cold, Took: 20 * time.Second})
	b.cfg.SwapCost = book

	holder := must(t, acquire(b, Request{Engine: "ninfer", Priority: Batch}))
	holder.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "ninfer-serve", Used: 22 * GiB})
	b.Tick(context.Background())

	if got := wait(t, b, "alpha", Interactive); got != "next in line: draining ninfer [batch, active]; start about 35 s once the card frees (measured)" {
		t.Fatalf("head reason = %q, want the place and its own measured start", got)
	}
	const second = "2nd in line (behind interactive alpha): waiting for ninfer [batch, draining] to free memory; start about 55 s once the card frees (1 start ahead, measured)"
	if got := wait(t, b, "beta", Interactive); got != second {
		t.Fatalf("second reason = %q, want %q", got, second)
	}
	const third = "3rd in line (behind interactive beta): waiting for ninfer [batch, draining] to free memory; start at least 55 s once the card frees (2 starts ahead, 1 unmeasured)"
	if got := wait(t, b, "gamma", Batch); got != third {
		t.Fatalf("third reason = %q, want %q", got, third)
	}
	const fourth = "4th in line (behind batch gamma): waiting for ninfer [batch, draining] to free memory; start at least 55 s once the card frees (3 starts ahead, 2 unmeasured)"
	if got := wait(t, b, "delta", Batch); got != fourth {
		t.Fatalf("fourth reason = %q, want %q", got, fourth)
	}

	empty, err := swapcost.NewBook("")
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.SwapCost = empty
	const fifth = "5th in line (behind batch delta): waiting for ninfer [batch, draining] to free memory; its start is unmeasured"
	if got := wait(t, b, "epsilon", Batch); got != fifth {
		t.Fatalf("fifth reason = %q, want %q", got, fifth)
	}
}

// Status carries the place and the measured start for every waiting lease,
// and nothing of either for a lease that is granted.
func TestStatusCarriesPlaceAndEstimate(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, _ := setup(t, tree)

	book, err := swapcost.NewBook("")
	if err != nil {
		t.Fatal(err)
	}
	book.Record(swapcost.Sample{Engine: "alpha", Tier: swapcost.Cold, Took: 30 * time.Second})
	book.Record(swapcost.Sample{Engine: "alpha", Tier: swapcost.Cold, Took: 40 * time.Second})
	book.Record(swapcost.Sample{Engine: "beta", Tier: swapcost.Cold, Took: 20 * time.Second})
	b.cfg.SwapCost = book

	holder := must(t, acquire(b, Request{Engine: "ninfer", Priority: Batch}))
	holder.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "ninfer-serve", Used: 22 * GiB})
	b.Tick(context.Background())

	wait(t, b, "alpha", Interactive)
	wait(t, b, "beta", Interactive)
	wait(t, b, "gamma", Batch)

	st := b.Status()
	if len(st.Waiting) != 3 {
		t.Fatalf("waiting = %+v, want three leases", st.Waiting)
	}
	want := []struct {
		engine    string
		position  int
		estimateS float64
	}{
		{"alpha", 1, 35},
		{"beta", 2, 55},
		{"gamma", 3, 55},
	}
	for i, w := range want {
		got := st.Waiting[i]
		if got.Engine != w.engine || got.Position != w.position || got.EstimateS != w.estimateS {
			t.Fatalf("waiting[%d] = %+v, want %s position %d estimate %v", i, got, w.engine, w.position, w.estimateS)
		}
	}
	for _, l := range st.Leases {
		if l.Position != 0 || l.EstimateS != 0 {
			t.Fatalf("granted lease %+v carries a place or estimate, want neither", l)
		}
	}
}
