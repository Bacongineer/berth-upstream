// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// The descendant record: every descendant of an attached lease's wrapper,
// keyed by PID plus start time, so a child that escapes before any GPU poll
// still belongs to the lease. The fake process table answers procs.Proc;
// the probe is the card in broker_test.go.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
)

func TestEscapedChildBeforeAnyGPUPollStaysUnderTheLease(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100}, Starts: map[int]uint64{100: 10, 101: 11}}
	table := []procs.Proc{
		{PID: 100, Parent: 1, Start: 10, Name: "berth"},
		{PID: 101, Parent: 100, Start: 11, Name: "hog"},
	}
	b, c, r := setup(t, tree)
	b.cfg.ReleaseGrace = time.Millisecond
	b.cfg.ProcTable = func() []procs.Proc { return table }

	l := must(t, acquire(b, Request{Engine: "a"}))
	l.Attach(100)
	b.Tick(context.Background()) // the child is recorded while the wrapper lives

	// The child takes the card and the wrapper dies before the next read:
	// 101 reparents and 100 is gone.
	c.set(gpu.Proc{PID: 101, Name: "hog", Used: 8 * GiB})
	tree.Parents[101] = 1
	delete(tree.Starts, 100)
	delete(tree.Parents, 100)
	table = table[1:]
	l.Release()
	b.Tick(context.Background())
	time.Sleep(5 * time.Millisecond)
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "stuck" || st.Leases[0].Used != 8*GiB {
		t.Fatalf("leases = %+v, want one stuck lease holding the escaped child", st.Leases)
	}
	if len(st.Squatters) != 0 || strings.Join(r.seen, ",") != "" {
		t.Fatalf("squatters = %+v, reports = %v", st.Squatters, r.seen)
	}

	reasons := make(chan string, 8)
	ch := make(chan result, 1)
	go func() {
		l, err := b.Acquire(context.Background(), Request{Engine: "b"}, func(s string) { reasons <- s })
		ch <- result{l, err}
	}()
	select {
	case why := <-reasons:
		if !strings.Contains(why, "pid 101 (hog) 8192 MiB") {
			t.Fatalf("wait reason %q does not name the escaped child", why)
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter heard nothing")
	}

	c.set()
	b.Tick(context.Background())
	must(t, ch)
}

// Entries whose PID plus start time no longer exists are pruned, so the
// record holds only what could still answer.
func TestDescendantRecordsPruneWhenTheProcessGoes(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100}, Starts: map[int]uint64{100: 10, 101: 11}}
	table := []procs.Proc{
		{PID: 100, Parent: 1, Start: 10, Name: "berth"},
		{PID: 101, Parent: 100, Start: 11, Name: "hog"},
	}
	b, _, _ := setup(t, tree)
	b.cfg.ProcTable = func() []procs.Proc { return table }

	l := must(t, acquire(b, Request{Engine: "a"}))
	l.Attach(100)
	b.Tick(context.Background())
	b.mu.Lock()
	desc := b.leases["a-1"].desc
	_, recorded := desc[ident{pid: 101, start: 11}]
	b.mu.Unlock()
	if !recorded || len(desc) != 1 || len(table) != 2 {
		t.Fatalf("descendants = %v, want exactly the child", desc)
	}

	// The child exits while the wrapper keeps running.
	table = table[:1]
	b.Tick(context.Background())
	b.mu.Lock()
	desc = b.leases["a-1"].desc
	b.mu.Unlock()
	if len(desc) != 0 {
		t.Fatalf("descendants = %v, want the record pruned", desc)
	}
}
