// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every source file names its copyright holder and license in its first
// lines, so a copy of one file still says where it came from.
func TestSourceFilesCarryTheLicenseHeader(t *testing.T) {
	const holder, spdx = "Copyright 2026 Trevin Tindall (T-Crypt)", "SPDX-License-Identifier: Apache-2.0"
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		switch filepath.Ext(path) {
		case ".go", ".py", ".sh":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		head := string(b[:min(len(b), 300)])
		if !strings.Contains(head, holder) || !strings.Contains(head, spdx) {
			t.Errorf("%s: missing the header (%q, %q) in its first lines", path, holder, spdx)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
