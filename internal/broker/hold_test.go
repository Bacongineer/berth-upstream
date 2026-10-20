// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

// A hold drains even a foreground engine, and is granted once the card
// shows the engine's memory gone.
func TestHoldDrainsForeground(t *testing.T) {
	b, c, _ := setup(t, procs.Map{101: 100})
	eng := must(t, acquire(b, Request{Engine: "llama", Priority: Foreground}))
	eng.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "llama-server", Used: 20 * GiB})
	b.Tick(context.Background())

	hold := acquire(b, Request{Engine: "hold:game", Priority: Hold, Pinned: true})
	pending(t, hold)
	drained(t, eng)
	eng.Release()
	c.set()
	b.Tick(context.Background())
	must(t, hold)
}

// Nothing drains a pinned lease: not a foreground engine, not another hold.
func TestPinnedIsNeverDrained(t *testing.T) {
	b, _, _ := setup(t, procs.Map{})
	app := must(t, acquire(b, Request{Engine: "blender", Priority: Batch, Pinned: true}))

	for _, r := range []Request{
		{Engine: "llama", Priority: Foreground, Wait: 100 * time.Millisecond},
		{Engine: "hold:call", Priority: Hold, Pinned: true, Wait: 100 * time.Millisecond},
	} {
		_, err := b.Acquire(context.Background(), r, nil)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "blender") || !strings.Contains(err.Error(), "pinned") {
			t.Fatalf("%s: want a refusal naming the pinned holder, got %v", r.Engine, err)
		}
	}
	select {
	case why := <-app.Drains():
		t.Fatalf("pinned holder was drained: %s", why)
	default:
	}
	if st := b.Status(); len(st.Leases) != 1 || !st.Leases[0].Pinned || st.Leases[0].Priority != "batch" {
		t.Fatalf("status lease = %+v", st.Leases)
	}
}

func TestParseHoldPriority(t *testing.T) {
	if p, err := ParsePriority("hold"); err != nil || p != Hold || p.String() != "hold" {
		t.Fatalf("hold: %v %v", p, err)
	}
}

// A track claim's card memory is named for the claim, not reported as a
// stowaway, and still counts against what a sized request can get.
func TestTrackedMemoryIsClaimedNotSquatting(t *testing.T) {
	b, c, r := setup(t, procs.Map{501: 500})
	b.SetClaims(map[int]string{500: "blender"}, []proto.Claim{{Name: "blender", Action: "track", Active: true, PIDs: []int{500}}})
	c.set(gpu.Proc{PID: 501, Name: "blender", Used: 10 * GiB})
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Squatters) != 0 {
		t.Fatalf("tracked process reported as squatter: %+v", st.Squatters)
	}
	if len(st.Claims) != 1 || st.Claims[0].Used != 10*GiB {
		t.Fatalf("claims = %+v", st.Claims)
	}
	for _, s := range r.seen {
		if strings.HasPrefix(s, "appeared:") {
			t.Fatalf("squatter reported: %v", r.seen)
		}
	}
	_, err := b.Acquire(context.Background(), Request{Engine: "big", Needs: 20 * GiB, Wait: 100 * time.Millisecond}, nil)
	if err == nil || !strings.Contains(err.Error(), "claims hold 10240 MiB") {
		t.Fatalf("want a refusal counting the claim, got %v", err)
	}
	must(t, acquire(b, Request{Engine: "small", Needs: 8 * GiB}))
}

// A process seen as a squatter before the watcher names it moves to the claim.
func TestSquatterBecomesClaimed(t *testing.T) {
	b, c, _ := setup(t, procs.Map{})
	c.set(gpu.Proc{PID: 700, Name: "obs", Used: GiB})
	b.Tick(context.Background())
	if len(b.Status().Squatters) != 1 {
		t.Fatal("want the squatter first")
	}
	b.SetClaims(map[int]string{700: "obs"}, nil)
	b.Tick(context.Background())
	if n := len(b.Status().Squatters); n != 0 {
		t.Fatalf("squatters = %d after the claim named it", n)
	}
}
