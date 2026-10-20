// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// The contrib/chaos scenarios a fake GPU probe can run, as
// contrib/chaos/README.md describes them. The probe is card in
// broker_test.go; no real GPU is involved.

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

// Scenario 1, clean drain: a holder on the card is drained for a foreground
// requester, and the card is handed over only after the probe shows the
// holder's memory gone, not when its wrapper lets go of the lease.
func TestChaosScenario1CleanDrain(t *testing.T) {
	tree := procs.Map{101: 100}
	b, c, _ := setup(t, tree)
	_, live, stop := b.Events(0, true)
	defer stop()

	a := must(t, acquire(b, Request{Engine: "a"}))
	a.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "hog", Used: 8 * GiB})
	b.Tick(context.Background())

	breq := acquire(b, Request{Engine: "b", Priority: Foreground})
	pending(t, breq)
	if why := drained(t, a); !strings.Contains(why, "b") {
		t.Fatalf("drain reason %q does not name the requester", why)
	}

	// a gives the lease back, but the probe still shows its 8 GiB.
	a.Release()
	pending(t, breq)

	c.set()
	b.Tick(context.Background())
	must(t, breq)

	// The log the README quotes, for a: draining, released, freed.
	var evs []proto.Event
	for {
		select {
		case e := <-live:
			evs = append(evs, e)
			if e.Kind == proto.EvGranted && e.Engine == "b" {
				var chain []string
				for _, x := range evs {
					if x.Engine != "a" {
						continue
					}
					switch x.Kind {
					case proto.EvDraining, proto.EvReleased, proto.EvFreed:
						chain = append(chain, x.Kind)
					}
				}
				want := []string{proto.EvDraining, proto.EvReleased, proto.EvFreed}
				if !slices.Equal(chain, want) {
					t.Fatalf("event chain for a = %v, want %v", chain, want)
				}
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("b was never granted; events = %v", evs)
		}
	}
}

// Scenario 8, containerized engine: a host ComfyUI and a vLLM in a docker
// container hand the card back and forth. The container's GPU process is
// parented by containerd-shim, so the process tree cannot reach it; the
// lease names the container instead, and the process belongs to it from its
// first sighting. Nothing is ever a stowaway, and the next grant waits for
// the probe to show the container's memory gone.
func TestChaosScenario8ContainerHandoff(t *testing.T) {
	tree := procs.Fake{Parents: map[int]int{101: 100, 102: 1}, Starts: map[int]uint64{100: 1, 101: 1, 102: 2}}
	b, c, r := setup(t, tree)
	b.cfg.Containers = func(pid int) (string, bool) {
		if pid == 201 {
			return vllmID, true
		}
		return "", false
	}

	comfy := must(t, acquire(b, Request{Engine: "comfyui", Needs: 8 * GiB}))
	comfy.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "python", Used: 8 * GiB})
	b.Tick(context.Background())

	vllm := acquire(b, Request{Engine: "vllm", Container: "vllm", Priority: Foreground})
	pending(t, vllm)
	if why := drained(t, comfy); !strings.Contains(why, "vllm") {
		t.Fatalf("drain reason %q does not name the requester", why)
	}
	comfy.Release()
	c.set()
	b.Tick(context.Background())
	vl := must(t, vllm)
	vl.Attach(102) // the docker CLI; containerd-shim parents the engine
	vl.AttachContainer(vllmID)

	// The containerized engine loads: not a tree descendant of the docker
	// CLI, attributed to the lease by container id all the same.
	c.set(gpu.Proc{PID: 201, Name: "vllm", Used: 16 * GiB})
	b.Tick(context.Background())
	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].Engine != "vllm" || st.Leases[0].Used != 16*GiB {
		t.Fatalf("leases = %+v, want the container under the vllm lease", st.Leases)
	}
	if len(st.Squatters) != 0 || strings.Join(r.seen, ",") != "" {
		t.Fatalf("squatters = %+v, reports = %v", st.Squatters, r.seen)
	}

	// And back: berth run's drain stopped the container, so the card
	// shows only ComfyUI again, and the wait was until that showed.
	comfy2 := acquire(b, Request{Engine: "comfyui2", Needs: 8 * GiB, Priority: Foreground})
	pending(t, comfy2)
	if why := drained(t, vl); !strings.Contains(why, "comfyui2") {
		t.Fatalf("drain reason %q does not name the requester", why)
	}
	vl.Release()
	pending(t, comfy2) // the container's 16 GiB still held

	c.set(gpu.Proc{PID: 101, Name: "python", Used: 8 * GiB})
	b.Tick(context.Background())
	must(t, comfy2)
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].Engine != "comfyui2" {
		t.Fatalf("leases = %+v, want the handoff back to ComfyUI", st.Leases)
	}
}

// Scenario 4, stowaway: a process holds VRAM without a lease. A sized
// request that no longer fits waits and is told why, the stowaway counts
// against every grant, and nothing signals it: there is no lease to drain,
// so the report is all the broker does.
func TestChaosScenario4Stowaway(t *testing.T) {
	b, c, r := setup(t, procs.Map{})
	c.set(gpu.Proc{PID: 9, Name: "hog", Used: 8 * GiB})
	b.Tick(context.Background())
	b.Tick(context.Background())

	reasons := make(chan string, 8)
	ch := make(chan result, 1)
	go func() {
		l, err := b.Acquire(context.Background(), Request{Engine: "b", Needs: 20000 << 20}, func(s string) { reasons <- s })
		ch <- result{l, err}
	}()
	pending(t, ch)
	select {
	case why := <-reasons:
		if !strings.Contains(why, "squatters hold 8192 MiB") {
			t.Fatalf("wait reason %q does not name the stowaway", why)
		}
	case <-time.After(time.Second):
		t.Fatal("the waiter was told nothing")
	}

	st := b.Status()
	if len(st.Leases) != 0 {
		t.Fatalf("a contested card granted %+v", st.Leases)
	}
	if len(st.Squatters) != 1 || st.Squatters[0].Name != "hog" || st.Squatters[0].PID != 9 {
		t.Fatalf("squatters = %+v", st.Squatters)
	}

	if got := strings.Join(r.seen, ","); got != "appeared:hog" {
		t.Fatalf("reports = %q, want a single appearance", got)
	}
}
