// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// What reclaim names: the processes a stuck lease recorded, and the ones
// nothing recorded.

import (
	"context"
	"testing"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

func TestReclaimListsRecordedAndStrangers(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100}, Starts: map[int]uint64{100: 10, 101: 11}}
	b, c, _ := setup(t, tree)
	l := must(t, acquire(b, Request{Engine: "a"}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "hog", Used: 8 * GiB}, gpu.Proc{PID: 9, Name: "orphan", Used: 1 * GiB})
	b.Tick(context.Background())
	l.Release()
	b.Tick(context.Background())

	hs, err := b.Reclaim("a-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []proto.Holder{
		{PID: 9, Name: "orphan", Used: 1 * GiB},
		{PID: 101, PIDStart: 11, Lease: "a-1", Name: "hog", Used: 8 * GiB},
	}
	if len(hs) != len(want) {
		t.Fatalf("holders = %+v", hs)
	}
	for i, h := range hs {
		if h != want[i] {
			t.Fatalf("holder %d = %+v, want %+v", i, h, want[i])
		}
	}
}

func TestReclaimRefusesWrongLeases(t *testing.T) {
	b, _, _ := setup(t, procs.Map{})
	must(t, acquire(b, Request{Engine: "a"}))
	if _, err := b.Reclaim("a-1"); err == nil {
		t.Fatal("an active lease was reclaimable")
	}
	if _, err := b.Reclaim("nope"); err == nil {
		t.Fatal("an unknown lease was reclaimable")
	}
	if hs, err := b.Reclaim(""); err != nil || len(hs) != 0 {
		t.Fatalf("empty target = %+v, %v, want no holders and no error", hs, err)
	}
}

// Without a lease named, reclaim covers stuck leases only: a merely
// releasing lease is still on its way out and is not the user's to
// reclaim. Named, a releasing lease works.
func TestReclaimWithoutALeaseNamedIsStuckOnly(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100}, Starts: map[int]uint64{100: 10, 101: 11}}
	b, c, _ := setup(t, tree)
	l := must(t, acquire(b, Request{Engine: "a"}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "hog", Used: 8 * GiB})
	b.Tick(context.Background())
	l.Release()

	if hs, err := b.Reclaim(""); err != nil || len(hs) != 0 {
		t.Fatalf("empty target = %+v, %v, want the releasing lease skipped", hs, err)
	}
	if hs, err := b.Reclaim("a-1"); err != nil || len(hs) != 1 || hs[0].Lease != "a-1" {
		t.Fatalf("named target = %+v, %v, want the releasing lease's holder", hs, err)
	}
}

// A card PID the lease once held, reused by something else, is not the
// lease's any more: it comes back as a stranger, never as a candidate.
func TestReclaimSkipsAReusedPID(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100}, Starts: map[int]uint64{100: 10, 101: 11}}
	b, c, _ := setup(t, tree)
	l := must(t, acquire(b, Request{Engine: "a"}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "hog", Used: 8 * GiB})
	b.Tick(context.Background())
	l.Release()

	// 101 exits; the kernel hands the PID to something else entirely.
	tree.Parents[101] = 1
	tree.Starts[101] = 99
	hs, err := b.Reclaim("a-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].Lease != "" || hs[0] != (proto.Holder{PID: 101, PIDStart: 99, Name: "hog", Used: 8 * GiB}) {
		t.Fatalf("holders = %+v, want only the stranger", hs)
	}
}
