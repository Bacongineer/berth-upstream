// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Containerized engines: a GPU process inside a container is parented by
// containerd-shim, so the process tree cannot reach it. A lease that names
// the container owns its processes anyway, and a Compose lease owns the
// processes of every container of its project; a container no lease names is
// a stowaway, named in the report.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
)

const (
	vllmID = "9b5f6c4d3e2a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f"
	// A Compose project's other two containers.
	ocrID     = "5f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f9b5f6c4d3e2a1b0f9e8d7c6b5a"
	sidecarID = "3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d"
)

// A Compose engine is one lease over several containers: the GPU process in
// each of them is the lease's, and none of them is a stowaway.
func TestComposeLeaseOwnsEveryContainerOfItsProject(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, r := setup(t, tree)
	b.cfg.Containers = func(pid int) (string, bool) {
		switch pid {
		case 201:
			return vllmID, true
		case 202:
			return ocrID, true
		}
		return "", false
	}

	l := must(t, acquire(b, Request{Engine: "paddleocr", Needs: 16 * GiB}))
	l.Attach(100) // the compose CLI; containerd-shim parents the services, not it
	l.AttachContainers([]string{vllmID, ocrID})
	c.set(
		gpu.Proc{PID: 201, Name: "paddle", Used: 6 * GiB},
		gpu.Proc{PID: 202, Name: "det", Used: 3 * GiB},
	)
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].Used != 9*GiB {
		t.Fatalf("leases = %+v, want both containers under the one lease", st.Leases)
	}
	if got := strings.Join(st.Leases[0].Containers, ","); got != vllmID+","+ocrID {
		t.Fatalf("lease containers = %q, want the whole project", got)
	}
	if len(st.Squatters) != 0 || len(r.seen) != 0 {
		t.Fatalf("squatters = %+v, reports = %v", st.Squatters, r.seen)
	}
}

func TestContainerEngineBelongsToItsLease(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, r := setup(t, tree)
	b.cfg.Containers = func(pid int) (string, bool) {
		if pid == 201 {
			return vllmID, true
		}
		return "", false
	}

	l := must(t, acquire(b, Request{Engine: "vllm", Containers: []string{"vllm"}, Needs: 16 * GiB}))
	l.Attach(100) // the docker CLI; containerd-shim parents the engine, not it
	l.AttachContainers([]string{vllmID})
	c.set(
		gpu.Proc{PID: 201, Name: "vllm", Used: 18 * GiB},
	)
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].Used != 18*GiB {
		t.Fatalf("leases = %+v, want the container's memory under the lease", st.Leases)
	}
	if !slices.Equal(st.Leases[0].Containers, []string{vllmID}) {
		t.Fatalf("lease = %+v, want the container it was resolved to", st.Leases[0])
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
	l := must(t, acquire(b, Request{Engine: "vllm", Containers: []string{"leaker"}}))
	l.AttachContainers([]string{vllmID})
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
	l, err := b.Reattach(Request{Engine: "vllm", Containers: []string{vllmID}}, 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	c.set(gpu.Proc{PID: 201, Name: "vllm", Used: 18 * GiB})
	b.Tick(context.Background())
	if st := b.Status(); len(st.Leases) != 1 || !slices.Equal(st.Leases[0].Containers, []string{vllmID}) ||
		st.Leases[0].Used != 18*GiB {
		t.Fatalf("leases = %+v, want the reattached lease owning the container", st.Leases)
	}
	_ = l
}

// WSL2 gives Berth no per-container GPU number: every container of the VM
// shows up as the VM's one process row. With exactly one container lease on
// the card that row is the engine's memory, and the lease counts the larger
// of its needs and the row.
func TestWSLVMRowCountsForTheOnlyContainerLease(t *testing.T) {
	b, c, r := setup(t, procs.Map{})
	b.cfg.Containers = func(int) (string, bool) { return "", false } // no cgroups to read

	l := must(t, acquire(b, Request{Engine: "pet-tagger", Containers: []string{vllmID}, Needs: 2 * GiB}))
	l.AttachContainers([]string{vllmID})
	c.set(gpu.Proc{PID: 4, Name: "vmwp.exe", Used: 20 * GiB})
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].Used != 20*GiB {
		t.Fatalf("leases = %+v, want the VM's 20 GiB under the container lease", st.Leases)
	}
	if len(st.Squatters) != 0 || len(r.seen) != 0 {
		t.Fatalf("squatters = %+v, reports = %v, want the VM row never a stowaway", st.Squatters, r.seen)
	}

	// The budget is the larger of needs and the row: with 20 GiB measured on
	// a 24 GiB card there is no room for a 6 GiB waiter, even though the
	// engine only asked for 2 GiB.
	next := acquire(b, Request{Engine: "next", Needs: 6 * GiB})
	b.Tick(context.Background())
	pending(t, next)
}

// Two container leases share the VM's one row, and Berth cannot split it.
// Each counts its needs, and the row stays a stowaway counted once.
func TestWSLVMRowBelongsToNeitherOfTwoContainerLeases(t *testing.T) {
	b, c, _ := setup(t, procs.Map{})
	b.cfg.Containers = func(int) (string, bool) { return "", false }
	a := must(t, acquire(b, Request{Engine: "tagger", Needs: 3 * GiB}))
	a.AttachContainers([]string{vllmID})
	z := must(t, acquire(b, Request{Engine: "ocr", Needs: 4 * GiB}))
	z.AttachContainers([]string{sidecarID})

	c.set(gpu.Proc{PID: 4, Name: "vmwp.exe", Used: 6 * GiB})
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Squatters) != 1 || st.Squatters[0].Name != "vmwp.exe" || st.Squatters[0].Used != 6*GiB {
		t.Fatalf("squatters = %+v, want the VM row counted once and owned by neither", st.Squatters)
	}
	for _, l := range st.Leases {
		if l.Used != 0 {
			t.Fatalf("lease %s = %d MiB used, want none of the VM row charged to it", l.ID, l.Used/GiB)
		}
	}
}

// A host lease is not a container lease: a WSL VM row beside it stays a
// stowaway, and nothing of the VM is charged to the engine on the card.
func TestWSLVMRowIsNotAHostLease(t *testing.T) {
	b, c, _ := setup(t, procs.Map{})
	b.cfg.Containers = func(int) (string, bool) { return "", false }
	must(t, acquire(b, Request{Engine: "ninfer", Needs: 4 * GiB}))
	c.set(gpu.Proc{PID: 4, Name: "vmwp.exe", Used: 6 * GiB})
	b.Tick(context.Background())
	st := b.Status()
	if len(st.Squatters) != 1 {
		t.Fatalf("squatters = %+v, want the VM row a stowaway", st.Squatters)
	}
	if len(st.Leases) != 1 || st.Leases[0].Used != 0 {
		t.Fatalf("leases = %+v, want none of it under the host lease", st.Leases)
	}
}

// The WSL VM's row stays on the card while the VM runs, after its containers
// stop. A container lease that owned the row is still freed when it
// releases: the row goes back to being a stowaway, counted against the card,
// instead of holding the lease releasing until it is reported stuck.
func TestReleasedContainerLeaseLetsGoOfTheWSLVMRow(t *testing.T) {
	b, c, _ := setup(t, procs.Map{})
	b.cfg.Containers = func(int) (string, bool) { return "", false }
	l := must(t, acquire(b, Request{Engine: "pet-tagger", Containers: []string{vllmID}, Needs: 2 * GiB}))
	l.AttachContainers([]string{vllmID})
	c.set(gpu.Proc{PID: 4, Name: "vmwp.exe", Used: 6 * GiB})
	b.Tick(context.Background())

	l.Release()
	c.set(gpu.Proc{PID: 4, Name: "vmwp.exe", Used: gpu.MiB})
	b.Tick(context.Background())
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 0 {
		t.Fatalf("leases = %+v, want the released lease freed", st.Leases)
	}
	if len(st.Squatters) != 1 || st.Squatters[0].Name != "vmwp.exe" || st.Squatters[0].Used != gpu.MiB {
		t.Fatalf("squatters = %+v, want the VM row back as a stowaway", st.Squatters)
	}
}
