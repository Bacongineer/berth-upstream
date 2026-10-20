// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pagecache

import (
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fadvWillNeed is POSIX_FADV_WILLNEED from posix_fadvise(2). The x/sys copy
// of the constant is generated under cgo, so the number stands here where it
// is used.
const fadvWillNeed = 3

// mincoreTries is how many times a mincore the kernel refuses is retried. On
// a machine that runs at its memory limit the kernel says "temporarily" and
// means it: a listing that measured nothing would read its whole library as
// unknown, when a moment of patience gets the answer.
const mincoreTries = 4

// mincoreDelay is how long one such retry waits for the kernel to come up.
const mincoreDelay = 100 * time.Millisecond

// resident measures one open file's page cache: each slice is mapped
// read-only, checked with mincore, and unmapped again. PROT_READ keeps the
// check from pulling a page in.
func resident(f *os.File, size int64) (int64, error) {
	ps := int64(unix.Getpagesize())
	var cached int64
	for off := int64(0); off < size; off += chunk {
		pages := (min(off+chunk, size) - off + ps - 1) / ps
		data, err := unix.Mmap(int(f.Fd()), off, int(pages*ps), unix.PROT_READ, unix.MAP_SHARED)
		if err != nil {
			return cached, fmt.Errorf("map %s: %w", f.Name(), err)
		}
		vec := make([]byte, pages)
		// mincore checks a region of the process's address space, so the
		// mapping's own base address is the offset, not the file's.
		if err = mincoreChecked(data, vec); err != nil {
			_ = unix.Munmap(data)
			return cached, fmt.Errorf("mincore %s: %w", f.Name(), err)
		}
		_ = unix.Munmap(data)
		for _, b := range vec {
			// Bit 0 is presence: a swapped-out or mlocked page that is
			// still in memory sets it.
			if b&0x1 != 0 {
				cached += ps
			}
		}
	}
	// A file's last page holds more than its tail: the file has only size
	// bytes to be resident.
	if cached > size {
		cached = size
	}
	return cached, nil
}

// mincoreChecked runs mincore over the mapping and retries the refusals the
// kernel makes when it is short of resources: ENOMEM and EAGAIN both say
// "temporarily" in the kernel's own words.
func mincoreChecked(data []byte, vec []byte) error {
	for try := 0; ; try++ {
		_, _, errno := unix.Syscall6(unix.SYS_MINCORE,
			uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)),
			uintptr(unsafe.Pointer(&vec[0])), 0, 0, 0)
		if errno == 0 {
			return nil
		}
		if errno != unix.ENOMEM && errno != unix.EAGAIN {
			return errno
		}
		if try == mincoreTries-1 {
			return errno
		}
		time.Sleep(mincoreDelay)
	}
}

// prefetch asks for the file in chunks, which keeps each ask small and lets
// the kernel spread the reads over its background schedule.
func prefetch(f *os.File, size int64) error {
	for off := int64(0); off < size; off += chunk {
		if err := unix.Fadvise(int(f.Fd()), off, min(off+chunk, size)-off, fadvWillNeed); err != nil {
			return fmt.Errorf("fadvise %s: %w", f.Name(), err)
		}
	}
	return nil
}
