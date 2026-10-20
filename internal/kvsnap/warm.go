// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package kvsnap

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/T-Crypt/berth/internal/pagecache"
)

// Warm asks the kernel to read the key's snapshot files into the page
// cache, so a restore on this machine reads from RAM while the kernel
// keeps the pages. The RAM tier for a snapshot is the page cache, and the
// pages are the kernel's: it gives them back when the machine needs the
// RAM, and the restore then reads that byte from the SSD again. A warm
// file is cached, never reserved. A platform without the kernel's page
// cache interface reports its own error, which is not forwarded here, so
// warming stays best effort.
func (x *Index) Warm(key Key) error {
	ss, err := x.For(key)
	if err != nil {
		return err
	}
	if x.Prefetch == nil {
		return fmt.Errorf("%s has no prefetcher wired", x.Dir)
	}
	for _, s := range ss {
		if err := x.Prefetch(filepath.Join(x.Dir, s.File)); err != nil && !errors.Is(err, pagecache.ErrUnsupported) {
			return err
		}
	}
	return nil
}
