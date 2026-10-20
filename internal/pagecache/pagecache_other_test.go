// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pagecache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOffLinuxIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resident(path); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Resident err = %v, want ErrUnsupported", err)
	}
	if err := Prefetch(path); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Prefetch err = %v, want ErrUnsupported", err)
	}
}
