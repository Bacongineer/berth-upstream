// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pagecache

import "os"

// The kernel's page-cache interface is Linux's. Off it, nothing can be
// measured or asked, so both hooks report ErrUnsupported and a caller reads
// it as "unknown".
func resident(f *os.File, size int64) (int64, error) {
	return 0, ErrUnsupported
}

func prefetch(f *os.File, size int64) error {
	return ErrUnsupported
}
