// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package storage

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// fsMagic names the Linux filesystems Berth expects under a model root.
var fsMagic = map[int64]string{
	0xEF53:     "ext4", // ext2, ext3 and ext4 share the magic
	0x9123683E: "btrfs",
	0x534C4653: "ntfs3",
	0x2011BAB0: "exfat",
	0x0857205A: "xfs",
	0x794C7630: "overlay",
	0x01021994: "tmpfs",
	0x2FC12DC1: "zfs",
	0xF2F52010: "f2fs",
	0x6969:     "cifs",
	0x6B6F6272: "9p",
	0x9660:     "iso9660",
}

// fsSupport is what each named filesystem allows. A filesystem this table
// does not name supports nothing Berth will rely on, and neither does a
// root whose space could not be measured.
var fsSupport = map[string]Support{
	"ext4":    {Hardlink: true, Symlink: true},
	"btrfs":   {Hardlink: true, Symlink: true, Reflink: true},
	"ntfs3":   {Hardlink: true},
	"exfat":   {},
	"xfs":     {Hardlink: true, Symlink: true},
	"overlay": {Hardlink: true, Symlink: true},
	"tmpfs":   {Hardlink: true, Symlink: true},
	"zfs":     {Hardlink: true, Symlink: true, Reflink: true},
	"f2fs":    {Hardlink: true, Symlink: true, Reflink: true},
	"cifs":    {},
	"9p":      {Symlink: true},
	"iso9660": {},
}

// Info reads path's filesystem: its name, its space and what it supports.
// A path that is not there is the caller's error to report.
func Info(path string) (Root, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Root{}, err
	}
	fs := fsMagic[st.Type]
	if fs == "" {
		fs = fmt.Sprintf("0x%08x", uint32(st.Type))
	}
	total := uint64(st.Blocks) * uint64(st.Bsize)
	free := uint64(st.Bavail) * uint64(st.Bsize)
	r := Root{Path: path, FS: fs, Total: total, Free: free, Used: total - free}
	r.Support = fsSupport[fs]
	return r, nil
}
