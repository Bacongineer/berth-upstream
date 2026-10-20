// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Collisions-board persistence: the counters file and the writes behind it.

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/T-Crypt/berth/internal/fsx"
	"github.com/T-Crypt/berth/internal/proto"
)

func loadCounters(path string) (proto.Counters, error) {
	var c proto.Counters
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Since.IsZero() {
		return c, errors.New("no since time in the file")
	}
	return c, nil
}

// persist writes the board when a count changed since the last write. The
// write is a temp file renamed over the old one, so a crash mid-write leaves
// the previous board, never a torn one. It runs outside b.mu.
func (b *Broker) persist() {
	if b.cfg.CountersFile == "" {
		return
	}
	b.saveMu.Lock()
	defer b.saveMu.Unlock()
	b.mu.Lock()
	c := b.counters
	b.mu.Unlock()
	if c == b.saved {
		return
	}
	data, err := json.Marshal(c)
	if err == nil {
		err = fsx.WriteFile(b.cfg.CountersFile, data, 0o600)
	}
	if err != nil {
		if !b.saveFailed {
			b.log.Warn("collisions board not saved; it will not survive a restart", "file", b.cfg.CountersFile, "err", err)
			b.saveFailed = true
		}
		return
	}
	b.saveFailed = false
	b.saved = c
}
