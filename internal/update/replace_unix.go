// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

// ReplaceBinary takes the downloaded file beside the target and renames
// it over the target. The rename is atomic: a crash leaves either the
// old binary or the new one.
func ReplaceBinary(newPath, target string) error {
	if err := os.Chmod(newPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(newPath, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
