// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"sync"

	"github.com/T-Crypt/berth/internal/pagecache"
)

// cacheWorkers is how many models a listing checks at once. mincore reads the
// kernel's page table, not the disk, so four checks in flight keep a big
// library off the critical path without the machine doing more work than
// one check at a time.
const cacheWorkers = 4

// Cache measures how much of each model's files the kernel page cache holds
// and records it on the model: its own files, split parts included, and the
// vision projectors and drafts that go with it. A file the check cannot
// reach is unknown, not an error, so a half-downloaded shard leaves the rest
// of the model's share as measured. The page cache is the kernel's and gives
// the pages back when the machine needs the RAM, so the figures are an
// estimate of reload speed, never a reservation.
func Cache(ms []Model) {
	sem := make(chan struct{}, cacheWorkers)
	var wg sync.WaitGroup
	for i := range ms {
		wg.Add(1)
		sem <- struct{}{}
		go func(m *Model) {
			defer wg.Done()
			defer func() { <-sem }()
			cacheModel(m)
		}(&ms[i])
	}
	wg.Wait()
}

// cacheModel is one model's check: every file it loads, measured once each.
func cacheModel(m *Model) {
	files := make([]string, 0, len(m.Files)+len(m.Companions))
	files = append(files, m.Files...)
	files = append(files, m.Companions...)
	if len(files) == 0 {
		files = []string{m.Path}
	}
	var cached, measured uint64
	for _, f := range files {
		c, s, err := pagecache.Resident(f)
		if err != nil {
			continue
		}
		cached += uint64(c)
		measured += uint64(s)
	}
	if measured > 0 {
		m.CachedBytes, m.CachedSize = cached, measured
	}
}

// CachedShare is the model's resident share in percent, or -1 when the check
// reached none of its files and nothing can be said. The kernel takes the
// pages back at will, so the share moves with the machine, not with the
// model.
func (m Model) CachedShare() int {
	if m.CachedSize == 0 {
		return -1
	}
	return int(100 * m.CachedBytes / m.CachedSize)
}

// warm is the share that reads as a model the machine can restart quickly:
// half its bytes in memory.
const warm = 50

// Warmth names the tier a listing shows beside the share: COOL when half or
// more of the model's bytes sit in the page cache, COLD below that, and an
// empty string when the check measured nothing.
func (m Model) Warmth() string {
	share := m.CachedShare()
	if share < 0 {
		return ""
	}
	if share >= warm {
		return "COOL"
	}
	return "COLD"
}
