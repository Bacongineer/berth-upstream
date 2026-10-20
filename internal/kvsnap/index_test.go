// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package kvsnap

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/storage"
)

var (
	keyA = Key{Engine: "e1", Model: "/models/a.gguf"}
	keyB = Key{Engine: "e1", Model: "/models/b.gguf"}
	keyC = Key{Engine: "e2", Model: "/models/a.gguf"} // same model, another engine
)

// testIndex is an index over a scratch directory with counting stand-ins
// for the page cache and the root measurement.
func testIndex(t *testing.T) *Index {
	t.Helper()
	x := &Index{
		Dir:      t.TempDir(),
		Prefetch: func(string) error { return nil },
		Info:     func(string) (storage.Root, error) { return storage.Root{}, nil },
	}
	return x
}

func TestName(t *testing.T) {
	n := Name(keyA, 3)
	if ok, _ := regexp.MatchString(`^[0-9a-f]{16}_s3$`, n); !ok {
		t.Errorf("Name = %q, want a 16-hex-digit hash plus the slot id", n)
	}
	if Name(keyA, 3) != n {
		t.Errorf("Name is not stable: %q then %q", n, Name(keyA, 3))
	}
	if Name(keyA, 0) == Name(keyB, 0) {
		t.Errorf("two models share a file name: %q", n)
	}
	if Name(keyA, 0) == Name(keyC, 0) {
		t.Errorf("the same model on two engines shares a file name")
	}
	if filepath.Base(n) != n {
		t.Errorf("Name = %q carries a path", n)
	}
}

func TestRecordForRoundTrip(t *testing.T) {
	x := testIndex(t)
	at := time.Now().Truncate(time.Second)
	if err := x.Record(keyA, 0, Result{Tokens: 100, Bytes: 2000, Took: time.Millisecond}, at); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := x.Record(keyB, 0, Result{Tokens: 50, Bytes: 900, Took: 0}, at.Add(time.Minute)); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := x.For(keyA)
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("For(keyA) = %d entries, want 1", len(got))
	}
	if got[0].Slot != 0 || got[0].File != Name(keyA, 0) ||
		got[0].Tokens != 100 || got[0].Bytes != 2000 || !got[0].SavedAt.Equal(at) {
		t.Errorf("entry = %+v, want the recorded save", got[0])
	}

	// A second index over the same directory reads the ledger from disk.
	x2, err := New(x.Dir)
	if err != nil {
		t.Fatalf("New over a written directory: %v", err)
	}
	got2, err := x2.For(keyB)
	if err != nil {
		t.Fatalf("For after reload: %v", err)
	}
	if len(got2) != 1 || got2[0].Tokens != 50 || got2[0].Bytes != 900 {
		t.Errorf("reloaded entry = %+v, want the recorded save", got2)
	}
}

func TestRecordReplacesSameSlot(t *testing.T) {
	x := testIndex(t)
	at := time.Now().Truncate(time.Second)
	if err := x.Record(keyA, 0, Result{Tokens: 100, Bytes: 2000}, at); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyA, 0, Result{Tokens: 4217, Bytes: 9000}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := x.For(keyA)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("For = %d entries, want the one replacement", len(got))
	}
	if got[0].Tokens != 4217 || !got[0].SavedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("entry = %+v, want the newer save", got[0])
	}
}

func TestForNeverCrossesModels(t *testing.T) {
	x := testIndex(t)
	at := time.Now().Truncate(time.Second)
	if err := x.Record(keyA, 0, Result{Tokens: 100, Bytes: 2000}, at); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyC, 0, Result{Tokens: 50, Bytes: 900}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := x.For(keyA)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.Key != keyA {
			t.Errorf("For(keyA) carries %s on %s, another model's cache", s.Key.Model, s.Key.Engine)
		}
	}
}

func TestForNewestFirst(t *testing.T) {
	x := testIndex(t)
	at := time.Now().Truncate(time.Second)
	if err := x.Record(keyA, 0, Result{Tokens: 1, Bytes: 100}, at); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyA, 1, Result{Tokens: 1, Bytes: 200}, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyA, 2, Result{Tokens: 1, Bytes: 300}, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := x.For(keyA)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range got {
		if want := 2 - i; s.Slot != want {
			t.Fatalf("For[%d] = slot %d, want %d", i, s.Slot, want)
		}
	}
}

func TestEvictOrderAndBudget(t *testing.T) {
	x := testIndex(t)
	at := time.Now().Truncate(time.Second)
	saves := []struct {
		key  Key
		slot int
		size int64
		at   time.Time
	}{
		{keyA, 0, 1000, at}, // oldest, goes first
		{keyB, 0, 2000, at.Add(time.Minute)},
		{keyA, 1, 3000, at.Add(2 * time.Minute)},
		{keyB, 1, 4000, at.Add(3 * time.Minute)}, // newest, stays
	}
	for _, s := range saves {
		p := filepath.Join(x.Dir, Name(s.key, s.slot))
		if err := os.WriteFile(p, make([]byte, s.size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := x.Record(s.key, s.slot, Result{Tokens: 1, Bytes: s.size}, s.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(x.Dir, "foreign.bin"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := x.Evict(7500)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if n != 2 {
		t.Fatalf("Evict removed %d files, want the two least recently saved", n)
	}
	for _, s := range saves {
		p := filepath.Join(x.Dir, Name(s.key, s.slot))
		_, err := os.Stat(p)
		if s.slot == 0 && err == nil {
			t.Errorf("%s still on disk after Evict", s.key.Model)
		}
		if s.slot == 1 && os.IsNotExist(err) {
			t.Errorf("%s removed although it is the most recently saved", s.key.Model)
		}
	}
	if _, err := os.Stat(filepath.Join(x.Dir, "foreign.bin")); err != nil {
		t.Errorf("Evict touched a file the ledger does not list: %v", err)
	}
	both, err := x.For(keyA)
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 1 || both[0].Slot != 1 {
		t.Errorf("ledger for keyA = %+v, want the surviving slot 1", both)
	}
}

func TestEvictForeignAloneOverBudget(t *testing.T) {
	x := testIndex(t)
	at := time.Now().Truncate(time.Second)
	p := filepath.Join(x.Dir, Name(keyA, 0))
	if err := os.WriteFile(p, make([]byte, 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyA, 0, Result{Tokens: 1, Bytes: 1000}, at); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(x.Dir, "foreign.bin"), make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := x.Evict(100)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("Evict removed %d files, want the one listed file", n)
	}
	if _, err := os.Stat(filepath.Join(x.Dir, "foreign.bin")); err != nil {
		t.Errorf("Evict touched the foreign file: %v", err)
	}
	left, err := x.For(keyA)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("ledger = %+v, want empty", left)
	}
}

func TestPlan(t *testing.T) {
	x := testIndex(t)
	live := []Slot{
		{ID: 0, Tokens: 4217}, // idle, a count left over
		{ID: 1},               // idle, no count: its cache may still be full
		{ID: 2, Busy: true},   // busy: never saved mid-generation
	}
	got := x.Plan(keyA, live)
	want := []Slot{{ID: 0, Tokens: 4217}, {ID: 1}}
	if len(got) != len(want) {
		t.Fatalf("Plan = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Plan[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A save that wrote no tokens is an empty slot: its file and any older
// record of the slot go.
func TestRecordEmptySave(t *testing.T) {
	x := testIndex(t)
	at := time.Now()
	if err := os.WriteFile(filepath.Join(x.Dir, Name(keyA, 0)), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyA, 0, Result{Tokens: 900, Bytes: 5}, at); err != nil {
		t.Fatal(err)
	}
	if err := x.Record(keyA, 0, Result{}, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if ss, err := x.For(keyA); err != nil || len(ss) != 0 {
		t.Fatalf("For = %+v, %v; want nothing after an empty save", ss, err)
	}
	if _, err := os.Stat(filepath.Join(x.Dir, Name(keyA, 0))); !os.IsNotExist(err) {
		t.Fatalf("the empty save's file is still there: %v", err)
	}
}

func TestFloor(t *testing.T) {
	cases := []struct {
		name     string
		root     storage.Root
		need     uint64
		floor    float64
		want     bool
		wantWarn bool
	}{
		{"a small save above the floor", storage.Root{Path: "/r", FS: "ext4", Total: 1000, Free: 900}, 100, 0.2, false, false},
		{"a save that crosses the floor", storage.Root{Path: "/r", FS: "ext4", Total: 1000, Free: 900}, 850, 0.2, true, false},
		{"an unmeasurable root warns and proceeds", storage.Root{Path: "/r", FS: "unknown"}, 100, 0.2, false, true},
		{"a zero floor disables the guard", storage.Root{Path: "/r", FS: "ext4", Total: 1000, Free: 10}, 100, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := &Index{
				Dir:      t.TempDir(),
				Prefetch: func(string) error { return nil },
				Info:     func(string) (storage.Root, error) { return tc.root, nil },
			}
			under, warn := x.Floor(tc.need, tc.floor)
			if under != tc.want {
				t.Errorf("Floor = %v, want %v", under, tc.want)
			}
			if (warn != "") != tc.wantWarn {
				t.Errorf("warn = %q, wantWarn %v", warn, tc.wantWarn)
			}
		})
	}
}

func TestFloorWalksUpForTheRoot(t *testing.T) {
	x := testIndex(t)
	deep := filepath.Join(x.Dir, "nested", "dir")
	x.Dir = deep
	called := 0
	x.Info = func(p string) (storage.Root, error) {
		called++
		if p == deep {
			return storage.Root{}, os.ErrNotExist // the directory does not exist yet
		}
		return storage.Root{Path: p, FS: "ext4", Total: 1000, Free: 900}, nil
	}
	under, warn := x.Floor(100, 0.2)
	if under || warn != "" {
		t.Errorf("Floor = (%v, %q), want a quiet pass on the measurable parent", under, warn)
	}
	if called < 2 {
		t.Errorf("Info called %d times, want the walk to climb at least one level", called)
	}
}
