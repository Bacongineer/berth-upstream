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

// scopeOf is a process's Berth scope path, "" when it runs outside one.
func (b *Broker) scopeOf(pid int) string {
	s, ok := b.cfg.Scopes(pid)
	if !ok {
		return ""
	}
	return s
}
