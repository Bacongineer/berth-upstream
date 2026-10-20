// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Containerized engines: a GPU process inside a container is parented by
// containerd-shim, so the process tree cannot reach it. A lease that names
// the container owns its processes anyway; a container no lease names is a
// stowaway, named in the report.

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
)

const vllmID = "9b5f6c4d3e2a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f"

func TestContainerEngineBelongsToItsLease(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, r := setup(t, tree)
	b.cfg.Containers = func(pid int) (string, bool) {
		if pid == 201 {
			return vllmID, true
		}
		return "", false
	}

	l := must(t, acquire(b, Request{Engine: "vllm", Container: "vllm", Needs: 16 * GiB}))
	l.Attach(100) // the docker CLI; containerd-shim parents the engine, not it
	l.AttachContainer(vllmID)
	c.set(
		gpu.Proc{PID: 201, Name: "vllm", Used: 18 * GiB},
	)
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].Used != 18*GiB {
		t.Fatalf("leases = %+v, want the container's memory under the lease", st.Leases)
	}
	if st.Leases[0].Container != vllmID || st.Leases[0].ContainerName != "vllm" {
		t.Fatalf("lease = %+v, want its container and the name it was given", st.Leases[0])
	}
	if len(st.Squatters) != 0 || len(r.seen) != 0 {
		t.Fatalf("squatters = %+v, reports = %v", st.Squatters, r.seen)
	}

	// The container keeps holding memory after the wrapper releases: the
	// lease stays releasing until the card shows the memory gone.
	l.Release()
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].State != "releasing" {
		t.Fatalf("leases = %+v, want one still releasing", st.Leases)
	}
	next := acquire(b, Request{Engine: "next", Needs: 8 * GiB})
	b.Tick(context.Background())
	pending(t, next)

	c.set()
	b.Tick(context.Background())
	must(t, next)
}

// A container no lease names is a stowaway like any host process, with the
// container's short id and, when the runtime answers, its name.
func TestContainerStowawayIsNamed(t *testing.T) {
	b, c, _ := setup(t, procs.Map{})
	b.cfg.Containers = func(pid int) (string, bool) {
		if pid == 9 {
			return vllmID, true
		}
		return "", false
	}
	b.cfg.ContainerName = func(string) string { return "leaker" }

	c.set(gpu.Proc{PID: 9, Name: "vllm", Used: 2 * GiB})
	b.Tick(context.Background())
	for i := 0; i < 100; i++ {
		if st := b.Status(); len(st.Squatters) == 1 && st.Squatters[0].ContainerName != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	st := b.Status()
	if len(st.Squatters) != 1 {
		t.Fatalf("squatters = %+v, want one", st.Squatters)
	}
	if st.Squatters[0].Container != vllmID[:12] || st.Squatters[0].ContainerName != "leaker" {
		t.Fatalf("squatter = %+v, want the short id and the runtime's name", st.Squatters[0])
	}

	// A lease may still claim the same container later: the memory becomes
	// the lease's and the stowaway report is withdrawn.
	l := must(t, acquire(b, Request{Engine: "vllm", Container: "leaker"}))
	l.AttachContainer(vllmID)
	b.Tick(context.Background())
	st = b.Status()
	if len(st.Squatters) != 0 || len(st.Leases) != 1 {
		t.Fatalf("squatters = %+v leases = %+v, want the container claimed", st.Squatters, st.Leases)
	}
	if !slices.Contains(st.Leases[0].GPUPIDs, 9) {
		t.Fatalf("lease = %+v, want pid 9", st.Leases[0])
	}
}

// A host lease keeps behaving without a container: the fake lookup answers
// nothing and attribution runs through the tree as before.
func TestHostProcessNeedsNoContainer(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, _ := setup(t, tree)
	b.cfg.Containers = func(int) (string, bool) { return "", false }
	l := must(t, acquire(b, Request{Engine: "ninfer"}))
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "ninfer-serve", Used: 20 * GiB})
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].Used != 20*GiB {
		t.Fatalf("leases = %+v", st.Leases)
	}
}

// A reattached engine names its container again, so the new daemon's lease
// still owns the container's processes.
func TestReattachCarriesTheContainer(t *testing.T) {
	tree := procs.Fake{Starts: map[int]uint64{100: 7}}
	b, c, _ := setup(t, tree)
	b.cfg.Containers = func(pid int) (string, bool) {
		if pid == 201 {
			return vllmID, true
		}
		return "", false
	}
	l, err := b.Reattach(Request{Engine: "vllm", Container: vllmID}, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	c.set(gpu.Proc{PID: 201, Name: "vllm", Used: 18 * GiB})
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].Container != vllmID ||
		st.Leases[0].Used != 18*GiB {
		t.Fatalf("leases = %+v, want the reattached lease owning the container", st.Leases)
	}
	_ = l
}
