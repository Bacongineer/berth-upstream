// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Cgroup attribution. `berth run` starts its engine in a berth-run
// scope and the gateway starts its runs in berth-engine scopes; a
// cgroup is inherited on fork, so every process under a lease's
// recorded scope is the lease's, even a child that left the process
// tree before any tick could record it (chaos scenario 3). Nothing is
// signalled because of a scope that Berth did not start: the scope
// holds exactly the processes a Berth wrapper execed into it.

// scopeLease is the one live lease that recorded scope, nil when none
// did or when more than one did: a scope two leases share names neither,
// so attribution falls through to the process tree.
func (b *Broker) scopeLease(scope string) *lease {
	if scope == "" {
		return nil
	}
	var found *lease
	for _, l := range b.leases {
		if (l.state == active || l.state == draining || l.state == releasing) && l.scope == scope {
			if found != nil {
				return nil
			}
			found = l
		}
	}
	return found
}

// refreshScopes reads each attached engine's scope while its PID plus
// start time is still the engine: the engine enters its scope a moment
// after it starts, so the read at attach would name the wrapper's
// cgroup. Once the engine is gone the last scope read stays, which is
// what names a child it left behind. Called with b.mu held.
func (b *Broker) refreshScopes() {
	for _, l := range b.leases {
		if (l.state != active && l.state != draining && l.state != releasing) || l.pid <= 0 ||
			b.tree.StartTime(l.pid) != l.pidStart {
			continue
		}
		if s, ok := b.cfg.Scopes(l.pid); ok {
			l.scope = s
		}
	}
}

// scopeOf is a process's Berth scope path, "" when it runs outside one.
func (b *Broker) scopeOf(pid int) string {
	s, ok := b.cfg.Scopes(pid)
	if !ok {
		return ""
	}
	return s
}
