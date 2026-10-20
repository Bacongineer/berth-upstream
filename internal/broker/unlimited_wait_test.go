// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"testing"
	"time"
)

func TestNegativeWaitHasNoDeadline(t *testing.T) {
	b, _, _ := setup(t, nil)
	b.cfg.DefaultWait = time.Millisecond
	holder := must(t, acquire(b, Request{Engine: "pinned-game", Priority: Foreground, Pinned: true}))
	defer holder.Release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	resultCh := make(chan result, 1)
	go func() {
		l, err := b.Acquire(ctx, Request{Engine: "fixture-waiter", Priority: Interactive, Wait: -time.Second}, func(string) {
			select {
			case ready <- struct{}{}:
			default:
			}
		})
		resultCh <- result{l: l, err: err}
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("waiter did not reach the queue")
	}
	select {
	case r := <-resultCh:
		t.Fatalf("unlimited waiter returned before cancellation: %v", r.err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case r := <-resultCh:
		if r.err == nil {
			r.l.Release()
			t.Fatal("canceled waiter acquired a lease")
		}
	case <-time.After(time.Second):
		t.Fatal("unlimited waiter ignored cancellation")
	}
}
