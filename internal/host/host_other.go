// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package host

// MemAvailable has no reading off Unix, so it reports no bytes: a
// caller's guard fails open rather than pretending RAM it cannot see.
func MemAvailable(string) uint64 { return 0 }

// MemPressure has no reading off Unix, so it reports unsupported.
func MemPressure(string) (float64, bool) { return 0, false }

// Read has no counters off Unix, so the stats come back unsupported.
func (r *Reader) Read() Stats { return Stats{} }
