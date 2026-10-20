// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package swapcost predicts how long the next engine switch will take,
// from the switches that already happened.
//
// After a switch, the caller measures how long the incoming engine took
// from the card showing free to ready, and records that time in the book
// with the engine and the tier the engine sat in before the switch. The
// book keeps the last 20 times per engine and tier, and answers a switch
// estimate as their median: a median, not a mean, so one slow cold start
// does not pull a warm estimate up. A pair with no samples is unknown:
// the book does not fill it with a guess, so a figure it gives is one it
// has measured.
package swapcost

import (
	"time"
)

// Tier is where an engine's weights sit before a switch starts.
type Tier string

const (
	Hot  Tier = "hot"  // on the card and ready
	Warm Tier = "warm" // parked in host RAM, the process alive
	Cool Tier = "cool" // weights mostly in the kernel page cache
	Cold Tier = "cold" // on disk, a full reload
)

// Sample is one measured switch: the engine, the tier it sat in before the
// switch, and how long the switch took.
type Sample struct {
	Engine string
	Tier   Tier
	Took   time.Duration
	At     time.Time
}

// Estimate is the book's answer for one switch: the median of the times it
// measured for that engine and tier. Known is false when the book has no
// samples for the pair; its Took is then zero and is not a prediction.
type Estimate struct {
	Engine  string
	Tier    Tier
	Took    time.Duration
	Samples int
	Known   bool
}

// TierFor reads an engine's position as a tier. onCard says the engine
// holds the card; parked says its weights sit in host RAM; cachedFrac is
// the share of its model files the kernel page cache still holds, from the
// pagecache reader. Cool and cold split at half: a file the kernel mostly
// holds restarts from memory, and one it mostly gave back reads from disk.
func TierFor(parked, onCard bool, cachedFrac float64) Tier {
	if onCard {
		return Hot
	}
	if parked {
		return Warm
	}
	if cachedFrac >= 0.5 {
		return Cool
	}
	return Cold
}
