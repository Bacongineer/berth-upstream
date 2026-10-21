// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Scope attribution beyond the scenario: a process joins exactly the
// lease that recorded its scope, never a neighbour's, and a process
// outside every recorded scope stays a stranger.

import (
	"context"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
)

const (
	runScopeA = "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-a-1a2b3c4d.scope"
	runScopeB = "/user.slice/user-1000.slice/user@1000.service/app.slice/berth-run-b-9f8e7d6c.scope"
)

func TestScopeNeverCrossAttributes(t *testing.T) {
	tree := procs.Fake{
		Parents: map[int]int{100: 1, 110: 1, 112: 1, 113: 1},
		Starts:  map[int]uint64{100: 1, 110: 2, 112: 5, 113: 6},
	}
	b, c, r := setup(t, tree)
	b.cfg.Scopes = func(pid int) (string, bool) {
		switch pid {
		case 100:
			return runScopeA, true
		case 110, 112:
			return runScopeB, true
		}
		return "", false
	}

	a := must(t, acquire(b, Request{Engine: "a", Needs: 4 * GiB}))
	a.Attach(100)
	mb := must(t, acquire(b, Request{Engine: "mine", Needs: 4 * GiB}))
	mb.Attach(110)

	// 112 escaped inside b's scope; 113 runs in no scope at all.
	c.set(
		gpu.Proc{PID: 112, Name: "python", Used: 8 * GiB},
		gpu.Proc{PID: 113, Name: "hog", Used: 1 * GiB},
	)
	b.Tick(context.Background())

	st := b.Status()
	var byEngine = map[string]uint64{}
	for _, l := range st.Leases {
		byEngine[l.Engine] = l.Used
	}
	if len(st.Leases) != 2 || byEngine["a"] != 0 || byEngine["mine"] != 8*GiB {
		t.Fatalf("leases = %+v, want 112 only under its own scope's lease", st.Leases)
	}
	if len(st.Squatters) != 1 || st.Squatters[0].PID != 113 {
		t.Fatalf("squatters = %+v, want only the scopeless process", st.Squatters)
	}
	if got := strings.Join(r.seen, ","); got != "appeared:hog" {
		t.Fatalf("reports = %q, want only the scopeless process", got)
	}
}

func TestSharedScopeNamesNeither(t *testing.T) {
	tree := procs.Fake{
		Parents: map[int]int{100: 1, 110: 1, 112: 1},
		Starts:  map[int]uint64{100: 1, 110: 2, 112: 5},
	}
	b, c, _ := setup(t, tree)
	b.cfg.Scopes = func(pid int) (string, bool) { return runScopeA, true }

	a := must(t, acquire(b, Request{Engine: "a", Needs: 4 * GiB}))
	a.Attach(100)
	mb := must(t, acquire(b, Request{Engine: "mine", Needs: 4 * GiB}))
	mb.Attach(110)

	// 112 sits in the scope both leases recorded and descends from neither.
	c.set(gpu.Proc{PID: 112, Name: "python", Used: 8 * GiB})
	b.Tick(context.Background())

	st := b.Status()
	for _, l := range st.Leases {
		if l.Used != 0 {
			t.Fatalf("lease %s holds %d, want a shared scope to name neither lease", l.Engine, l.Used)
		}
	}
	if len(st.Squatters) != 1 || st.Squatters[0].PID != 112 {
		t.Fatalf("squatters = %+v, want 112 reported as a stowaway", st.Squatters)
	}
}
