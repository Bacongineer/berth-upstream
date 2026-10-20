// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package swapcost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func sample(engine string, tier Tier, took time.Duration) Sample {
	return Sample{Engine: engine, Tier: tier, Took: took, At: testNow}
}

func emptyBook() *Book {
	return &Book{rings: map[string]map[Tier][]Sample{}}
}

func TestTierFor(t *testing.T) {
	cases := []struct {
		name   string
		parked bool
		onCard bool
		cached float64
		want   Tier
	}{
		{"on the card wins", true, true, 1, Hot},
		{"on the card", false, true, 0, Hot},
		{"parked", true, false, 0, Warm},
		{"parked beats a cached share", true, false, 1, Warm},
		{"cool at half", false, false, 0.5, Cool},
		{"cool above half", false, false, 0.9, Cool},
		{"cold below half", false, false, 0.49, Cold},
		{"cold at zero", false, false, 0, Cold},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TierFor(c.parked, c.onCard, c.cached); got != c.want {
				t.Errorf("TierFor(parked=%v, onCard=%v, cached=%v) = %s, want %s", c.parked, c.onCard, c.cached, got, c.want)
			}
		})
	}
}

func TestBookRingKeepsLastTwenty(t *testing.T) {
	b := emptyBook()
	for i := 0; i < 25; i++ {
		b.Record(sample("llama", Warm, time.Duration(i+1)*time.Second))
	}
	es := b.Estimate("llama", Warm)
	// 25 times were recorded; the ring keeps the last 20, 6 s through 25 s.
	if es.Samples != 20 {
		t.Errorf("ring keeps %d samples, want %d", es.Samples, maxSamples)
	}
	if !es.Known {
		t.Errorf("a sampled pair is known, got %+v", es)
	}
	if want := 15500 * time.Millisecond; es.Took != want {
		t.Errorf("median of 6..25 s is %s, got %s", want, es.Took)
	}
}

func TestBookMedian(t *testing.T) {
	b := emptyBook()
	for _, took := range []time.Duration{10 * time.Second, 1 * time.Second, 5 * time.Second} {
		b.Record(sample("llama", Cool, took))
	}
	if es := b.Estimate("llama", Cool); es.Took != 5*time.Second || es.Samples != 3 || !es.Known {
		t.Errorf("odd median of 1, 5, 10 s = %+v, want 5 s", es)
	}
	for _, took := range []time.Duration{1 * time.Second, 10 * time.Second, 2 * time.Second, 3 * time.Second} {
		b.Record(sample("llama", Cold, took))
	}
	if es := b.Estimate("llama", Cold); es.Took != 2500*time.Millisecond {
		t.Errorf("even median of 1, 2, 3, 10 s = %s, want 2.5 s", es.Took)
	}
}

func TestBookHotIsAlwaysZero(t *testing.T) {
	b := emptyBook()
	// A stray hot sample is kept but cannot move the estimate.
	b.Record(sample("llama", Hot, 30*time.Second))
	if es := b.Estimate("llama", Hot); es.Took != 0 || !es.Known || es.Samples != 0 {
		t.Errorf("a sampled hot pair is zero and known, got %+v", es)
	}
	if es := b.Estimate("nobody", Hot); es.Took != 0 || !es.Known {
		t.Errorf("hot is zero and known with no samples, got %+v", es)
	}
}

func TestBookUnknownStaysUnknown(t *testing.T) {
	b := emptyBook()
	if es := b.Estimate("llama", Cold); es.Known || es.Samples != 0 || es.Took != 0 {
		t.Errorf("a pair with no samples is unknown, got %+v", es)
	}
	b.Record(sample("llama", Warm, 400*time.Millisecond))
	if es := b.Estimate("llama", Cold); es.Known {
		t.Errorf("samples in one tier do not make another tier known, got %+v", es)
	}
	if es := b.Estimate("other", Warm); es.Known {
		t.Errorf("samples in one engine do not make another engine known, got %+v", es)
	}
}

func TestBookMissingFileIsEmpty(t *testing.T) {
	b, err := NewBook(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing file is an empty book, got %v", err)
	}
	if es := b.Estimate("llama", Warm); es.Known {
		t.Errorf("an empty book has no estimates, got %+v", es)
	}
}

func TestBookCorruptFileIsEmptyBookWithAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	if err := os.WriteFile(path, []byte("not the book's json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := NewBook(path)
	if err == nil {
		t.Fatal("a corrupt file is an error, got none")
	}
	if b == nil {
		t.Fatal("a corrupt file still gives a book, so the caller can go on")
	}
	if es := b.Estimate("llama", Warm); es.Known || es.Samples != 0 {
		t.Errorf("a corrupt file starts an empty book, got %+v", es)
	}
}

func TestBookPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.json")
	b, err := NewBook(path)
	if err != nil {
		t.Fatal(err)
	}
	b.Record(sample("llama", Warm, 500*time.Millisecond))
	b.Record(sample("llama", Cool, 12*time.Second))
	b.Record(sample("ninfer", Cold, 40*time.Second))
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := NewBook(path)
	if err != nil {
		t.Fatalf("the saved book does not read back: %v", err)
	}
	cases := []struct {
		engine  string
		tier    Tier
		took    time.Duration
		samples int
		known   bool
	}{
		{"llama", Warm, 500 * time.Millisecond, 1, true},
		{"llama", Cool, 12 * time.Second, 1, true},
		{"llama", Hot, 0, 0, true},
		{"ninfer", Cold, 40 * time.Second, 1, true},
		{"ninfer", Warm, 0, 0, false},
		{"ghost", Cold, 0, 0, false},
	}
	for _, c := range cases {
		if es := got.Estimate(c.engine, c.tier); es.Took != c.took || es.Samples != c.samples || es.Known != c.known {
			t.Errorf("after a round trip Estimate(%s, %s) = %+v, want took %s samples %d known %v",
				c.engine, c.tier, es, c.took, c.samples, c.known)
		}
	}
}
