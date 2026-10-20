// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package procs

// System has no parent lookup on this platform, so only a
// wrapper's direct child PID is attributed to its lease.
type System struct{}

func (System) Parent(int) int       { return 0 }
func (System) StartTime(int) uint64 { return 0 }
