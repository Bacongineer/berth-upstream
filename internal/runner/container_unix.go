// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package runner

// The container steps run wherever the runtime CLI does; this release ships
// them for Linux.

func containerSupported() bool { return true }
