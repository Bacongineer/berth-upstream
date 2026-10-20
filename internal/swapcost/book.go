// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package swapcost

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/fsx"
)

// maxSamples is how many times the book keeps per engine and tier, the
// newest of them.
const maxSamples = 20

// Book keeps, per engine and tier, the last 20 measured switch times, and
// answers estimates for the next one.
type Book struct {
	path  string
	rings map[string]map[Tier][]Sample
}

// diskBook is the book's JSON shape: the samples per engine, per tier.
type diskBook struct {
	Samples map[string]map[Tier][]Sample `json:"samples,omitempty"`
}

// NewBook returns the book kept at path, so the learned times survive a
// restart of whatever records them. A missing file is an empty book. A
// file that is not the book's JSON is an error with an empty book: the
// caller logs it and goes on, the way the broker does with its footprint
// file.
func NewBook(path string) (*Book, error) {
	b := &Book{path: path, rings: map[string]map[Tier][]Sample{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return b, err
	}
	var disk diskBook
	if err := json.Unmarshal(data, &disk); err != nil {
		return b, err
	}
	for engine, tiers := range disk.Samples {
		b.rings[engine] = tiers
	}
	return b, nil
}

// Record adds one measured switch. It keeps at most maxSamples per engine
// and tier, dropping the oldest first.
func (b *Book) Record(s Sample) {
	tiers := b.rings[s.Engine]
	if tiers == nil {
		tiers = map[Tier][]Sample{}
		b.rings[s.Engine] = tiers
	}
	ring := append(tiers[s.Tier], s)
	if len(ring) > maxSamples {
		ring = ring[len(ring)-maxSamples:]
	}
	tiers[s.Tier] = ring
}

// Estimate answers what a switch to engine from tier is likely to take:
// the median of the times the book measured for that pair. Hot is always
// zero and known: an engine that already holds the card needs no switch.
// A pair with no samples is unknown, with zero time, and the book does not
// fill it with a guess.
func (b *Book) Estimate(engine string, tier Tier) Estimate {
	es := Estimate{Engine: engine, Tier: tier}
	if tier == Hot {
		es.Known = true
		return es
	}
	samples := b.rings[engine][tier]
	if len(samples) == 0 {
		return es
	}
	tooks := make([]time.Duration, len(samples))
	for i, s := range samples {
		tooks[i] = s.Took
	}
	sort.Slice(tooks, func(i, j int) bool { return tooks[i] < tooks[j] })
	n := len(tooks)
	if n%2 == 1 {
		es.Took = tooks[n/2]
	} else {
		es.Took = (tooks[n/2-1] + tooks[n/2]) / 2
	}
	es.Samples = n
	es.Known = true
	return es
}

// Save writes the book to its file with an atomic rename, so a crash
// leaves either the old file or the new one. A book with no path keeps
// nothing on disk.
func (b *Book) Save() error {
	if b.path == "" {
		return nil
	}
	data, err := json.Marshal(diskBook{Samples: b.rings})
	if err != nil {
		return err
	}
	return fsx.WriteFile(b.path, data, 0o600)
}
