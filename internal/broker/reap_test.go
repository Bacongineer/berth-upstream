// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// What the reaper decides: a granted lease whose attached process is gone
// gives its berth back on the broker's own ticks, without the wrapper's
// disconnect. And what it leaves alone: an attached process that is still
// alive, and a lease that names a container.

import (
	"context"
	"testing"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

func eventCount(b *Broker, kind, lease string) int {
	evs, _, _ := b.Events(0, false)
	n := 0
	for _, e := range evs {
		if e.Kind == kind && e.Lease == lease {
			n++
		}
	}
	return n
}

// A run whose tree is killed out from under it: the wrapper's disconnect
// never arrives, but the broker sees the attached process gone on its own
// ticks. One dead tick counts, two release the lease, and the next tick
// frees it, as a wrapper's release would.
func TestReaperFreesALeaseWhoseAttachedProcessDies(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{100: 10}}
	b, c, _ := setup(t, tree)
	b.cfg.ProcTable = func() []procs.Proc { return nil }
	l := must(t, acquire(b, Request{Engine: "a", Needs: 8 * GiB}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 100, Name: "a-engine", Used: 8 * GiB})
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].State != "active" {
		t.Fatalf("leases = %+v, want one active", st.Leases)
	}

	// The run's tree is force-killed; the driver shows the memory free.
	delete(tree.Starts, 100)
	c.set()
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].State != "active" {
		t.Fatalf("one dead tick released the lease: %+v", st.Leases)
	}
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].State != "releasing" {
		t.Fatalf("two dead ticks did not release the lease: %+v", st.Leases)
	}
	if n := eventCount(b, proto.EvOrphaned, "a-1"); n != 1 {
		t.Fatalf("orphaned events = %d, want 1", n)
	}
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 0 {
		t.Fatalf("leases = %+v, want none once the card shows it free", st.Leases)
	}
	if n := eventCount(b, proto.EvFreed, "a-1"); n != 1 {
		t.Fatalf("freed events = %d, want 1", n)
	}
}

// A PID the kernel handed to a stranger is not the engine: the identity is
// PID plus start time, so the reaper releases the lease while the
// stranger's memory stays a stowaway, never the lease's.
func TestReaperTreatsAReusedPIDAsGone(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{100: 10}}
	b, c, _ := setup(t, tree)
	b.cfg.ProcTable = func() []procs.Proc { return nil }
	l := must(t, acquire(b, Request{Engine: "a", Needs: 8 * GiB}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 100, Name: "a-engine", Used: 8 * GiB})
	b.Tick(context.Background())

	// The engine exits; the kernel hands the PID to something else.
	tree.Starts[100] = 99
	c.set(gpu.Proc{PID: 100, Name: "stranger", Used: 2 * GiB})
	b.Tick(context.Background())
	b.Tick(context.Background())
	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "releasing" || st.Leases[0].Used != 0 {
		t.Fatalf("leases = %+v, want the orphaned lease released, holding nothing", st.Leases)
	}
	if len(st.Squatters) != 1 || st.Squatters[0].Name != "stranger" {
		t.Fatalf("squatters = %+v, want the stranger on its own", st.Squatters)
	}
	if n := eventCount(b, proto.EvOrphaned, "a-1"); n != 1 {
		t.Fatalf("orphaned events = %d, want 1", n)
	}
}

// A lease whose attached process is alive is never touched by the reaper,
// not even when one tick's pid lookup goes wrong: the count resets the
// tick the process is seen again.
func TestReaperLeavesALiveAttachedProcess(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{100: 10}}
	b, c, _ := setup(t, tree)
	b.cfg.ProcTable = func() []procs.Proc { return nil }
	l := must(t, acquire(b, Request{Engine: "a", Needs: 8 * GiB}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 100, Name: "a-engine", Used: 8 * GiB})
	b.Tick(context.Background())

	// A hiccup: one tick the lookup misses the process.
	delete(tree.Starts, 100)
	b.Tick(context.Background())
	tree.Starts[100] = 10
	for i := 0; i < 3; i++ {
		b.Tick(context.Background())
	}
	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "active" {
		t.Fatalf("the reaper touched a live lease: %+v", st.Leases)
	}
	if n := eventCount(b, proto.EvOrphaned, "a-1"); n != 0 {
		t.Fatalf("orphaned events = %d, want none", n)
	}
}

// A lease that names a container is left to it: its processes belong to
// the container, not the attached PID, so a dead PID releases nothing.
func TestReaperLeavesAContainerLease(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{100: 10}}
	b, c, _ := setup(t, tree)
	b.cfg.Containers = func(pid int) (string, bool) { return "", false }
	b.cfg.ProcTable = func() []procs.Proc { return nil }
	l := must(t, acquire(b, Request{Engine: "a", Containers: []string{"a"}, Needs: 8 * GiB}))
	l.Attach(100)
	l.AttachContainers([]string{vllmID})
	c.set(gpu.Proc{PID: 100, Name: "a-engine", Used: 8 * GiB})
	b.Tick(context.Background())

	delete(tree.Starts, 100)
	c.set()
	b.Tick(context.Background())
	b.Tick(context.Background())
	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "active" {
		t.Fatalf("the reaper released a container lease: %+v", st.Leases)
	}
	if n := eventCount(b, proto.EvOrphaned, "a-1"); n != 0 {
		t.Fatalf("orphaned events = %d, want none", n)
	}
}

// A child that outlives its wrapper still counts under the lease while it
// holds the card: the reaper waits for the child too, and releases once the
// card shows nothing of the run left.
func TestReaperWaitsForTheRunsChildren(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100}, Starts: map[int]uint64{100: 10, 101: 11}}
	b, c, _ := setup(t, tree)
	table := []procs.Proc{
		{PID: 100, Parent: 1, Start: 10, Name: "berth"},
		{PID: 101, Parent: 100, Start: 11, Name: "a-engine"},
	}
	b.cfg.ProcTable = func() []procs.Proc { return table }
	l := must(t, acquire(b, Request{Engine: "a", Needs: 8 * GiB}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "a-engine", Used: 8 * GiB})
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].Used != 8*GiB {
		t.Fatalf("child not attributed to the lease: %+v", st.Leases)
	}

	// The wrapper dies; the child keeps the card.
	table = table[1:]
	delete(tree.Starts, 100)
	delete(tree.Parents, 100)
	b.Tick(context.Background())
	b.Tick(context.Background())
	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "active" || st.Leases[0].Used != 8*GiB {
		t.Fatalf("the reaper released a lease whose child still holds the card: %+v", st.Leases)
	}

	// The child goes too, and the card is empty: the record is pruned on
	// the same tick, and the reaper, already past its two dead ticks,
	// releases the run.
	table = []procs.Proc{}
	delete(tree.Starts, 101)
	delete(tree.Parents, 101)
	c.set()
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].State != "releasing" {
		t.Fatalf("the reaper did not release the emptied run: %+v", st.Leases)
	}
	if n := eventCount(b, proto.EvOrphaned, "a-1"); n != 1 {
		t.Fatalf("orphaned events = %d, want 1", n)
	}
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 0 {
		t.Fatalf("leases = %+v, want none once the card shows it free", st.Leases)
	}
}
