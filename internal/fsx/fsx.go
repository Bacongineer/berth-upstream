// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package fsx writes files that must survive a crash.
package fsx

import (
	"os"
	"path/filepath"
	"runtime"
)

// WriteFile writes data to path atomically: a temp file in the same
// directory, fsynced, then renamed over path, and the directory fsynced
// too, so a crash leaves either the old file or the new one, never a
// half-written one. perm is the file's mode. A missing directory is made
// 0o755 when the file is readable by others (a shared root's catalog),
// else 0o700. The rename over an existing file works on Windows.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm(perm)); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op after the rename
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

func dirPerm(perm os.FileMode) os.FileMode {
	if perm&0o044 != 0 {
		return 0o755
	}
	return 0o700
}

// syncDir records the rename in the directory entry. Windows cannot fsync
// a directory, and the rename is durable there without one.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}
