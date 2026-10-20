// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package runner

// Container engines are Linux-only in this release: --container is accepted
// and ignored, and the drain runs as it does for a host process.

func containerSupported() bool { return false }
