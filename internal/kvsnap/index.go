// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package kvsnap

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/fsx"
	"github.com/T-Crypt/berth/internal/pagecache"
	"github.com/T-Crypt/berth/internal/storage"
)

// ledgerFile is the index in the snapshot directory.
const ledgerFile = "index.json"

// Key names what a snapshot is for. Model is the model identity the caller
// passes, the model path or id the engine reports; the index never guesses
// it, so two models are never mixed.
type Key struct {
	Engine string `json:"engine"`
	Model  string `json:"model"`
}

// Snapshot is one saved slot cache and what the engine reported about it.
type Snapshot struct {
	Key     Key       `json:"key"`
	Slot    int       `json:"slot"`
	File    string    `json:"file"`
	Tokens  int       `json:"tokens"`
	Bytes   int64     `json:"bytes"`
	SavedAt time.Time `json:"saved_at"`
}

// Index is a snapshot directory and its ledger, the index.json in it that
// lists every file Berth wrote there. Files the ledger does not list are
// never touched.
type Index struct {
	Dir string
	// Prefetch warms a snapshot file into the kernel page cache; New wires
	// it to pagecache.Prefetch.
	Prefetch func(path string) error
	// Info measures the filesystem Dir sits on; New wires it to
	// storage.Info.
	Info func(path string) (storage.Root, error)
}

// New opens the snapshot directory's index. A missing ledger starts
// empty; a ledger that will not parse is an error, so a corrupt file is
// not silently dropped.
func New(dir string) (*Index, error) {
	x := &Index{Dir: dir, Prefetch: pagecache.Prefetch, Info: storage.Info}
	if _, err := x.read(); err != nil {
		return nil, err
	}
	return x, nil
}

// Name is the snapshot file name for key's slot: the key hashed, plus the
// slot id. The same key and slot always name the same file, and no part
// of a model path reaches the name.
func Name(key Key, slot int) string {
	h := sha256.Sum256([]byte(key.Engine + "\x00" + key.Model))
	return fmt.Sprintf("%x_s%d", h[:8], slot)
}

// Plan is which of the engine's live slots to save: every idle one. A drain
// asks only after the slot boundary, so the slots are idle then, and
// llama.cpp reports a slot's prompt tokens only while a task is attached, so
// an idle slot's count says nothing about its cache. The save itself says:
// Record keeps only a save that wrote tokens. A busy slot is never saved
// mid-generation.
func (x *Index) Plan(key Key, live []Slot) []Slot {
	out := []Slot{}
	for _, s := range live {
		if !s.Busy {
			out = append(out, s)
		}
	}
	return out
}

// Record lists a save: key's slot saved with r's tokens and bytes at time.
// A record under the same key and slot replaces its predecessor, since the
// engine overwrote the file; a save of an empty slot removes both.
func (x *Index) Record(key Key, slot int, r Result, at time.Time) error {
	ss, err := x.read()
	if err != nil {
		return err
	}
	kept := []Snapshot{}
	for _, s := range ss {
		if s.Key == key && s.Slot == slot {
			continue
		}
		kept = append(kept, s)
	}
	if r.Tokens == 0 {
		// An empty slot: the engine wrote a file with nothing to restore,
		// over any older snapshot of this slot. Neither is worth keeping.
		if err := os.Remove(filepath.Join(x.Dir, Name(key, slot))); err != nil && !os.IsNotExist(err) {
			return err
		}
		return x.write(kept)
	}
	kept = append(kept, Snapshot{Key: key, Slot: slot, File: Name(key, slot), Tokens: r.Tokens, Bytes: r.Bytes, SavedAt: at})
	return x.write(kept)
}

// For is the key's snapshots, newest first. Nothing for another model is
// ever in the answer, since the ledger lists each file under the key it was
// saved for.
func (x *Index) For(key Key) ([]Snapshot, error) {
	ss, err := x.read()
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, s := range ss {
		if s.Key == key {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].SavedAt.Equal(out[j].SavedAt) {
			return out[i].SavedAt.After(out[j].SavedAt)
		}
		return out[i].Slot < out[j].Slot
	})
	return out, nil
}

// Evict removes the least recently saved snapshots, in that order, until
// the files Berth wrote in the directory fit the budget in bytes. Files
// the ledger does not list are left alone, so if they alone exceed the
// budget every listed file is removed. A ledger entry for a file that is
// already gone is cleaned up. The removal count is reported.
func (x *Index) Evict(budget int64) (int, error) {
	ss, err := x.read()
	if err != nil {
		return 0, err
	}
	ordered := append([]Snapshot(nil), ss...)
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].SavedAt.Equal(ordered[j].SavedAt) {
			return ordered[i].SavedAt.Before(ordered[j].SavedAt)
		}
		return ordered[i].Slot < ordered[j].Slot
	})
	dirBytes, sizes, err := listedBytes(x.Dir, ss)
	if err != nil {
		return 0, err
	}
	removed := 0
	for i, s := range ordered {
		if dirBytes <= budget {
			break
		}
		if sz, onDisk := sizes[s.File]; onDisk {
			if err := os.Remove(filepath.Join(x.Dir, s.File)); err != nil && !os.IsNotExist(err) {
				return removed, fmt.Errorf("evict %s: %w", s.File, err)
			}
			dirBytes -= sz
		}
		removed++
		if err := x.write(ordered[i+1:]); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

// Floor reports whether saving need more bytes into the snapshot directory
// would leave its root under the library floor, a fraction of the root's
// total. The root is measured, never assumed; a root this system cannot
// measure is reported through warn, and the save proceeds.
func (x *Index) Floor(need uint64, floor float64) (under bool, warn string) {
	root, err := x.Info(x.Dir)
	if err != nil {
		// The directory may not exist yet: walk up to the root that does.
		p := x.Dir
		for {
			up := filepath.Dir(p)
			root, err = x.Info(p)
			if err == nil || up == p {
				break
			}
			p = up
		}
		if err != nil {
			return false, ""
		}
	}
	under, err = root.WouldGoUnderFloor(need, floor)
	if err != nil {
		if errors.Is(err, storage.ErrUndetectable) {
			return false, fmt.Sprintf("%s sits on %s, whose free space berth cannot measure here; the floor was not checked", root.Path, root.FS)
		}
		return false, ""
	}
	return under, ""
}

func (x *Index) ledgerPath() string {
	return filepath.Join(x.Dir, ledgerFile)
}

func (x *Index) read() ([]Snapshot, error) {
	b, err := os.ReadFile(x.ledgerPath())
	if os.IsNotExist(err) {
		return []Snapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	var ss []Snapshot
	if err := json.Unmarshal(b, &ss); err != nil {
		return nil, fmt.Errorf("%s: %w", x.ledgerPath(), err)
	}
	return ss, nil
}

func (x *Index) write(ss []Snapshot) error {
	b, err := json.MarshalIndent(ss, "", "  ")
	if err != nil {
		return err
	}
	// Owner-only: the ledger names the engine, model and slot of every
	// saved conversation.
	return fsx.WriteFile(x.ledgerPath(), append(b, '\n'), 0o600)
}

// listedBytes is the on-disk total of the files the ledger lists, with
// each file's size. A listed file that is gone keeps no entry.
func listedBytes(dir string, ss []Snapshot) (int64, map[string]int64, error) {
	sizes := make(map[string]int64, len(ss))
	want := make(map[string]bool, len(ss))
	for _, s := range ss {
		want[s.File] = true
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, sizes, nil
	}
	if err != nil {
		return 0, nil, err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() || !want[e.Name()] {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			return 0, nil, err
		}
		sizes[e.Name()] = fi.Size()
		total += fi.Size()
	}
	return total, sizes, nil
}
