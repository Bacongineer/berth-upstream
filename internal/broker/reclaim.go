// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// What reclaim works from: the card processes a stuck lease recorded. The
// broker only names them; the server's stopper decides about signaling, and
// only for what is recorded here.

import (
	"fmt"
	"sort"

	"github.com/T-Crypt/berth/internal/proto"
)

// StillAlive reports whether pid still runs as start. The reclaim stopper
// re-checks with it immediately before every signal, so a process that has
// exited, or a PID the kernel reused, is never signalled.
func (b *Broker) StillAlive(pid int, start uint64) bool {
	return start != 0 && b.tree.StartTime(pid) == start
}

// Reclaim names the card processes in question: what the named lease
// (which may still be releasing), or without an id every stuck lease,
// recorded, and what holds memory on the card that no lease recorded.
func (b *Broker) Reclaim(id string) ([]proto.Holder, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ids []string
	if id == "" {
		for key, l := range b.leases {
			if l.state == stuck {
				ids = append(ids, key)
			}
		}
		sort.Strings(ids)
	} else {
		l, ok := b.leases[id]
		if !ok {
			return nil, fmt.Errorf("no lease named %s", id)
		}
		if l.state != releasing && l.state != stuck {
			return nil, fmt.Errorf("lease %s is %s, not stuck", id, l.state)
		}
		ids = []string{id}
	}
	want := make(map[string]bool, len(ids))
	for _, key := range ids {
		want[key] = true
	}
	var hs []proto.Holder
	for _, p := range b.snap.Procs {
		pid := b.identify(p.PID)
		l := b.owner(pid, b.containerOf(p.PID), b.scopeOf(p.PID))
		switch {
		case l != nil && want[l.id]:
			hs = append(hs, proto.Holder{PID: p.PID, PIDStart: pid.start, Lease: l.id, Name: p.Name, Used: p.Used})
		case l == nil:
			hs = append(hs, proto.Holder{PID: p.PID, PIDStart: pid.start, Name: p.Name, Used: p.Used})
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i].PID < hs[j].PID })
	return hs, nil
}
