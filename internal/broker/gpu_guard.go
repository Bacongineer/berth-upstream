// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
)

// observationWait leaves NVIDIA's lease-table fallback unchanged. A guarded
// probe cannot grant from a failed/stale reading, including its first failure.
func (b *Broker) observationWait(now time.Time) string {
	if errors.Is(b.probeErr, gpu.ErrUnsafeRead) || (b.snap.Guarded && b.probeErr != nil) {
		return "waiting for a successful GPU observation"
	}
	if b.snap.Guarded && (b.snap.At.IsZero() || (b.snap.MaxAge > 0 && now.Sub(b.snap.At) > b.snap.MaxAge)) {
		return "waiting for a fresh GPU observation"
	}
	return ""
}

func (b *Broker) leaseUnobserved(l *lease) bool {
	if !b.snap.Guarded {
		return false
	}
	for _, pid := range b.snap.Unobserved {
		if pid == l.pid {
			return true
		}
		for id := range l.gpu {
			if id.pid == pid {
				return true
			}
		}
		for id := range l.desc {
			if id.pid == pid {
				return true
			}
		}
	}
	return false
}

// driverReleased supplements process-list evidence. The card must return to
// its pre-load unattributed baseline after accounting for currently visible
// processes. A different engine freeing memory must not mask this engine's
// lingering buffers. Shared/unobservable allocations may delay a handoff.
func (b *Broker) driverReleased(l *lease) bool {
	if !b.snap.Guarded {
		return true
	}
	return !b.leaseUnobserved(l) && b.snap.Baseline() <= l.releaseBaseline
}

// retainUnobserved keeps already attributed memory on unreadable PIDs. New,
// unattributed memory stays in the driver's baseline, never fabricated free.
func (b *Broker) retainUnobserved(pids []int, present map[ident]bool, claimed map[ident]claimedProc) {
	for _, l := range b.leases {
		for id := range l.gpu {
			if slices.Contains(pids, id.pid) {
				present[id] = true
			}
		}
	}
	for id := range b.squatters {
		if slices.Contains(pids, id.pid) {
			present[id] = true
		}
	}
	for id, p := range b.claimed {
		if slices.Contains(pids, id.pid) {
			present[id] = true
			claimed[id] = p
		}
	}
}

// guardedDetail is useful in stuck reports without inventing an owner for
// memory whose process is no longer observable.
func (b *Broker) guardedDetail(l *lease) string {
	if b.snap.Guarded && len(l.gpu) == 0 {
		return fmt.Sprintf("driver reports %d MiB unattributed (pre-load baseline %d MiB)", b.snap.Baseline()/gpu.MiB, l.releaseBaseline/gpu.MiB)
	}
	return holding(l)
}
