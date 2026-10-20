// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Footprints: the VRAM each engine's leases held at their peak, as the
// driver measured it, kept across restarts. They are shown as an estimate
// of what a run needs; Berth never treats them as a reservation.

import (
	"encoding/json"
	"os"
	"time"

	"github.com/T-Crypt/berth/internal/fsx"
)

// engineFootprint is one engine's record of its runs: the most memory seen
// across them, and the last run's peak, which a next run most likely needs.
type engineFootprint struct {
	Peak uint64    // the most memory any run was measured holding
	Last uint64    // the last run's peak
	Runs int       // runs measured holding memory
	At   time.Time // when Last was recorded
}

// footprints is the table keyed by engine key: the lease's group, else its
// engine name.
type footprints map[string]*engineFootprint

// loadFootprints reads the table from its file. A missing file is an empty
// table; a corrupt one is reported by the caller and ignored.
func loadFootprints(path string) (footprints, error) {
	f := footprints{}
	data, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	return f, json.Unmarshal(data, &f)
}

// foldFootprint records the peak a freed lease measured, under the key it
// counted for. A lease that never held memory measured nothing and is not a
// run. b.mu must be held.
func (b *Broker) foldFootprint(l *lease, now time.Time) {
	if l.peak == 0 {
		return
	}
	k := l.key()
	f := b.footprints[k]
	if f == nil {
		f = &engineFootprint{}
		b.footprints[k] = f
	}
	if l.peak > f.Peak {
		f.Peak = l.peak
	}
	f.Last = l.peak
	f.Runs++
	f.At = now
	b.footprintDirty = true
}

// Footprint reports the VRAM a run of the engine last used: the most recent
// measured peak, what a next run most likely needs. ok is false until the
// broker has measured the engine. The engine is named by its key: the
// lease's group, else its engine name.
func (b *Broker) Footprint(engine string) (bytes uint64, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if f := b.footprints[engine]; f != nil && f.Last > 0 {
		return f.Last, true
	}
	return 0, false
}

// persistFootprints writes the table when a fold changed it since the last
// write, the way persist does for the board: a temp file renamed over the
// old one, outside b.mu.
func (b *Broker) persistFootprints() {
	if b.cfg.FootprintFile == "" {
		return
	}
	b.footSaveMu.Lock()
	defer b.footSaveMu.Unlock()
	b.mu.Lock()
	if !b.footprintDirty {
		b.mu.Unlock()
		return
	}
	data, err := json.Marshal(b.footprints)
	b.footprintDirty = false
	b.mu.Unlock()
	if err == nil {
		err = fsx.WriteFile(b.cfg.FootprintFile, data, 0o600)
	}
	if err != nil {
		if !b.footSaveFailed {
			b.log.Warn("engine footprints not saved; the estimates will not survive a restart", "file", b.cfg.FootprintFile, "err", err)
			b.footSaveFailed = true
		}
		return
	}
	b.footSaveFailed = false
}
