// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package update

import (
	"fmt"
	"os"
)

// ReplaceBinary moves the running target aside to target.old and renames
// the new file into its place: Windows will not overwrite a running
// .exe, but it will rename one.
func ReplaceBinary(newPath, target string) error {
	old := target + ".old"
	if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear %s: %w", old, err)
	}
	if err := os.Rename(target, old); err != nil {
		return fmt.Errorf("move %s aside: %w", target, err)
	}
	if err := os.Rename(newPath, target); err != nil {
		// the running binary must not be left behind
		if rerr := os.Rename(old, target); rerr != nil {
			return fmt.Errorf("replace %s: %v; restoring it failed too: %w", target, err, rerr)
		}
		return err
	}
	return nil
}
