// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package pagecache measures how much of a file's pages the kernel page cache
// holds, and asks the kernel to warm a file back into it.
//
// The page cache is the kernel's, not Berth's: it keeps the pages until the
// machine needs the RAM, and gives them back when it does. A resident share
// says a re-read is likely to come from memory, not that any RAM is held.
//
// Resident reads the page table the kernel keeps for the mapping (mincore)
// and never a byte of the file, so checking warmth cannot pull pages in. On
// a machine running at its memory limit the kernel may refuse the check
// while it is short of resources; Resident retries a few times before
// reporting the file as unknown.
//
// Prefetch asks the kernel to read the file ahead (posix_fadvise WILLNEED)
// and returns as soon as the ask is made; the reads run on the kernel's
// schedule and commit no RAM.
//
// The interface is Linux's. Elsewhere both functions return ErrUnsupported,
// which callers read as "unknown", not as a failure.
package pagecache

import (
	"errors"
	"fmt"
	"os"
)

// chunk is the most one mapping holds at a time: a 70 GB file is checked in
// slices, so the measurement never needs a 70 GB mapping or one vector.
const chunk = 1 << 30

// ErrUnsupported is what the platform without the kernel's page-cache
// interface reports. A listing shows such a model as unknown, not failed.
var ErrUnsupported = errors.New("this system has no page-cache interface")

// Resident reports how many bytes of path the kernel page cache holds right
// now, with the file's size. It reads the page table, not the file, so the
// check neither pulls a page in nor pushes one out. A file with no pages to
// count reports 0 of 0 with no error.
func Resident(path string) (cached, size int64, err error) {
	f, err := open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return 0, 0, fmt.Errorf("%s is a directory", path)
	}
	c, err := resident(f, fi.Size())
	return c, fi.Size(), err
}

// Prefetch asks the kernel to read path into the page cache in the
// background. It returns once the ask is made, which is at once; the reads
// follow on the kernel's schedule and hold no RAM of Berth's.
func Prefetch(path string) error {
	f, err := open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return prefetch(f, fi.Size())
}

func open(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}
