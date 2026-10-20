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
