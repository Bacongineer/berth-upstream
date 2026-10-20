// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
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
