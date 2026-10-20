// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package storage reports what a model root sits on: the filesystem, the
// space left, and what that filesystem actually supports. A pull is refused
// before it starts when it would leave the root under its floor, so a drive
// never fills.
package storage

import (
	"errors"
	"fmt"
)

// ErrUndetectable means the root's space cannot be measured on this system,
// so a floor cannot be checked. Callers warn and proceed: a guard that
// cannot measure must not pretend it guards.
var ErrUndetectable = errors.New("free space cannot be measured here")

// Support is what the root's filesystem allows, which decides how a
// migration may move files: hardlink where it is safe, never a symlink on a
// root another OS reads.
type Support struct {
	Hardlink bool `json:"hardlink"`
	Symlink  bool `json:"symlink"`
	Reflink  bool `json:"reflink"`
}

// Root is one place a shelf may live and what it is.
type Root struct {
	Path string `json:"path"`
	// FS is the filesystem's name (ext4, btrfs, ntfs3, ...); "unknown" when
	// this system cannot name it.
	FS string `json:"fs"`
	// Total and Free are the whole filesystem's space in bytes, Free being
	// what an unprivileged user may still take.
	Total   uint64  `json:"total"`
	Free    uint64  `json:"free"`
	Used    uint64  `json:"used"`
	Support Support `json:"supports"`
}

// Detectable is whether this root's space was measured at all.
func (r Root) Detectable() bool { return r.Total > 0 }

// WouldGoUnderFloor says whether putting need more bytes on the root would
// leave it below floorFrac of its total. A floor of 0 disables the guard.
// When the root's space cannot be measured the answer is ErrUndetectable,
// which a caller turns into a warning, not a refusal.
func (r Root) WouldGoUnderFloor(need uint64, floorFrac float64) (bool, error) {
	if floorFrac < 0 {
		return false, fmt.Errorf("floor %g is negative", floorFrac)
	}
	if floorFrac > 1 {
		return false, fmt.Errorf("floor %g is over the whole root", floorFrac)
	}
	if !r.Detectable() {
		return false, ErrUndetectable
	}
	if floorFrac == 0 {
		return false, nil
	}
	floor := uint64(float64(r.Total) * floorFrac)
	return r.Free < need+floor, nil
}

// FloorBytes is how much of the root's total a floor of frac keeps free.
func (r Root) FloorBytes(frac float64) uint64 {
	if frac <= 0 || !r.Detectable() {
		return 0
	}
	return uint64(float64(r.Total) * frac)
}
