// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// fsSupport is what each named filesystem allows, mirroring the Linux
// table for the names GetVolumeInformation returns. A filesystem this
// table does not name supports nothing Berth will rely on. Windows
// symlinks need an admin token or Developer Mode, so none is allowed.
var fsSupport = map[string]Support{
	"NTFS":  {Hardlink: true},
	"ReFS":  {},
	"exFAT": {},
	"FAT32": {},
	"FAT":   {},
}

// Info reads path's filesystem: its name, its space and what it
// supports. A path that is not there is the caller's error to report.
func Info(path string) (Root, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Root{}, err
	}
	if !fi.IsDir() {
		return Root{}, errors.New(path + " is not a directory")
	}
	var avail, total, free uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Root{}, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return Root{}, fmt.Errorf("%s: %w", path, err)
	}
	r := Root{Path: path, FS: fsName(path), Total: total, Free: avail, Used: total - avail}
	r.Support = fsSupport[r.FS]
	return r, nil
}

// fsName is the volume's filesystem name ("NTFS") or "unknown" when the
// volume could not be asked.
func fsName(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return "unknown"
	}
	root, err := windows.UTF16PtrFromString(vol + `\`)
	if err != nil {
		return "unknown"
	}
	var buf [windows.MAX_PATH + 1]uint16
	err = windows.GetVolumeInformation(root, nil, 0, nil, nil, nil, &buf[0], uint32(len(buf)))
	if err != nil {
		return "unknown"
	}
	return windows.UTF16ToString(buf[:])
}
