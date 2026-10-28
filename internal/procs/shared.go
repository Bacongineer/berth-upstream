// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

import (
	"sync"
	"time"
)

// Shared wraps list so it reads the process table at most once per maxAge.
// berthd gives one to every card's broker: their ticks land together, and
// one table serves them all instead of one /proc walk per card. Callers
// only read the table.
func Shared(list func() []Proc, maxAge time.Duration) func() []Proc {
	var mu sync.Mutex
	var table []Proc
	var at time.Time
	return func() []Proc {
		mu.Lock()
		defer mu.Unlock()
		if now := time.Now(); table == nil || now.Sub(at) >= maxAge {
			table, at = list(), now
		}
		return table
	}
}
