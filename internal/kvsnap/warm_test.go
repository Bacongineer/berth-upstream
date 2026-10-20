// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package kvsnap

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/pagecache"
	"github.com/T-Crypt/berth/internal/storage"
)

func TestWarmCallsPrefetchOnTheKeyFiles(t *testing.T) {
	var warmed []string
	x := &Index{
		Dir:      t.TempDir(),
		Prefetch: func(p string) error { warmed = append(warmed, p); return nil },
		Info:     func(string) (storage.Root, error) { return storage.Root{}, nil },
	}
	at := time.Now().Truncate(time.Second)
	for _, slot := range []int{0, 1} {
		if err := x.Record(keyA, slot, Result{Bytes: 100}, at.Add(time.Duration(slot)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(x.Dir, "foreign.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := x.Warm(keyA); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	want := []string{
		filepath.Join(x.Dir, Name(keyA, 1)), // newest first
		filepath.Join(x.Dir, Name(keyA, 0)),
	}
	if len(warmed) != len(want) {
		t.Fatalf("Prefetch called on %v, want %v", warmed, want)
	}
	for i := range want {
		if warmed[i] != want[i] {
			t.Errorf("warm[%d] = %s, want %s", i, warmed[i], want[i])
		}
	}
}

func TestWarmToleratesTheOffLinuxPageCache(t *testing.T) {
	x := &Index{
		Dir:      t.TempDir(),
		Prefetch: func(string) error { return pagecache.ErrUnsupported },
		Info:     func(string) (storage.Root, error) { return storage.Root{}, nil },
	}
	if err := x.Record(keyA, 0, Result{Bytes: 100}, time.Now().Truncate(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := x.Warm(keyA); err != nil {
		t.Errorf("Warm = %v, want the unsupported platform to pass quietly", err)
	}
}
