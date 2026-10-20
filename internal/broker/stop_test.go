// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// What Stop asks: the engine's attached leases, by name or by group. And
// what it refuses: an engine no lease answers to.

import (
	"context"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

// Three leases share the card: two with a run attached, one without. Stop
// asks the attached ones, by name and by group, and refuses the rest.
func TestStopAsksTheEnginesLeases(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{101: 1, 102: 2}}
	b, c, _ := setup(t, tree)
	a := must(t, acquire(b, Request{Engine: "a", Needs: 8 * GiB}))
	a.Attach(101)
	fam := must(t, acquire(b, Request{Engine: "b", Group: "fam", Needs: 8 * GiB}))
	fam.Attach(102)
	must(t, acquire(b, Request{Engine: "c", Needs: 8 * GiB}))
	c.set(gpu.Proc{PID: 101, Name: "a", Used: 8 * GiB}, gpu.Proc{PID: 102, Name: "b", Used: 8 * GiB})
	b.Tick(context.Background())

	// A second ask while the reason is still on its way is dropped: the
	// lease is already being stopped, and it is booked only once.
	for _, want := range []string{"a", "a"} {
		ids, err := b.Stop(want)
		if err != nil {
			t.Fatalf("stop(%s): %v", want, err)
		}
		if len(ids) != 1 || ids[0] != "a-1" {
			t.Fatalf("stop(%s) = %v, want [a-1]", want, ids)
		}
	}
	if n := drainingEvents(b, "a-1"); n != 1 {
		t.Fatalf("a-1 draining events = %d, want 1", n)
	}
	if why := drained(t, a); why != "stopped by berth stop" {
		t.Fatalf("drain reason = %q", why)
	}

	ids, err := b.Stop("fam")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "b-2" {
		t.Fatalf("stop(fam) = %v, want [b-2]", ids)
	}
	if why := drained(t, fam); why != "stopped by berth stop" {
		t.Fatalf("drain reason = %q", why)
	}

	st := b.Status()
	if len(st.Leases) != 3 || st.Leases[0].State != "draining" || st.Leases[1].State != "draining" || st.Leases[2].State != "active" {
		t.Fatalf("states = %+v", st.Leases)
	}
	if st.Counters.Drains != 2 {
		t.Fatalf("drains = %d, want 2", st.Counters.Drains)
	}
	for _, name := range []string{"c", "nobody"} {
		if _, err := b.Stop(name); err == nil || !strings.Contains(err.Error(), "no running engine named "+name+" under a lease") {
			t.Fatalf("stop(%s) err = %v, want the no-running-engine error", name, err)
		}
	}
}

func drainingEvents(b *Broker, lease string) int {
	evs, _, _ := b.Events(0, false)
	n := 0
	for _, e := range evs {
		if e.Kind == proto.EvDraining && e.Lease == lease {
			n++
		}
	}
	return n
}

// The pin keeps the scheduler off a lease; a person who runs berth stop
// means stop, and the lease is asked like any other.
func TestStopOverridesAPinnedLease(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{101: 1}}
	b, c, _ := setup(t, tree)
	a := must(t, acquire(b, Request{Engine: "a", Pinned: true, Needs: 8 * GiB}))
	a.Attach(101)
	c.set(gpu.Proc{PID: 101, Name: "a", Used: 8 * GiB})
	b.Tick(context.Background())

	ids, err := b.Stop("a")
	if err != nil || len(ids) != 1 || ids[0] != "a-1" {
		t.Fatalf("stop = %v, %v", ids, err)
	}
	if why := drained(t, a); why != "stopped by berth stop" {
		t.Fatalf("drain reason = %q", why)
	}
}

// A released lease is not a target: whatever its processes still hold is
// berth reclaim's business, and Stop says so rather than asking twice.
func TestStopRefusesAReleasedLease(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{101: 1}}
	b, c, _ := setup(t, tree)
	a := must(t, acquire(b, Request{Engine: "a", Needs: 8 * GiB}))
	a.Attach(101)
	c.set(gpu.Proc{PID: 101, Name: "a", Used: 8 * GiB})
	b.Tick(context.Background())
	a.Release()
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "releasing" {
		t.Fatalf("state after release = %+v", st.Leases)
	}
	if _, err := b.Stop("a"); err == nil || !strings.Contains(err.Error(), "no running engine named a under a lease") {
		t.Fatalf("stop err = %v, want the no-running-engine error", err)
	}
}
