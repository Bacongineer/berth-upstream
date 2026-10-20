// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Descendant recording. Once per tick, every descendant of an attached
// lease's wrapper is recorded under that lease, keyed by PID plus start
// time. A child that forks, leaves the process group and reparents before
// any GPU poll then still answers owner(): the lease goes stuck with the
// child's name instead of reporting the child as a stowaway. One table scan
// serves the whole tick.

import (
	"github.com/T-Crypt/berth/internal/procs"
)

// recordDescendants reads the process table once and refreshes every
// lease's descendant set: entries live until their PID plus start time no
// longer does. Called with b.mu held.
func (b *Broker) recordDescendants() {
	attached := false
	for _, l := range b.leases {
		if (l.state == active || l.state == draining) && l.pid > 0 &&
			b.tree.StartTime(l.pid) == l.pidStart {
			attached = true
			break
		}
	}
	if !attached {
		return
	}
	table := b.cfg.ProcTable()
	if table == nil {
		return
	}
	live := make(map[int]procs.Proc, len(table))
	for _, p := range table {
		live[p.PID] = p
	}
	for _, l := range b.leases {
		for id := range l.desc {
			if p, ok := live[id.pid]; !ok || p.Start != id.start {
				delete(l.desc, id)
			}
		}
	}
	kids := make(map[int][]procs.Proc, len(table))
	for _, p := range table {
		kids[p.Parent] = append(kids[p.Parent], p)
	}
	for _, l := range b.leases {
		if l.state != active && l.state != draining {
			continue
		}
		root, ok := live[l.pid]
		if !ok || root.Start != l.pidStart {
			continue
		}
		queue := append([]procs.Proc(nil), kids[l.pid]...)
		for seen := map[int]bool{l.pid: true}; len(queue) > 0; {
			p := queue[0]
			queue = queue[1:]
			if seen[p.PID] {
				continue
			}
			seen[p.PID] = true
			l.desc[ident{pid: p.PID, start: p.Start}] = struct{}{}
			queue = append(queue, kids[p.PID]...)
		}
	}
}
