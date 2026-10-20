// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package gpu

// withPDH is the identity off Windows: only the driver's own numbers are
// read there.
func withPDH(s Snapshot) Snapshot { return s }
