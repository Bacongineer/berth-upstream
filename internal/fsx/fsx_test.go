// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package fsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		path string
		perm os.FileMode
	}{
		{name: "new file in a new directory", path: filepath.Join(dir, "made", "up", "state.json"), perm: 0o600},
		{name: "file beside nothing yet", path: filepath.Join(dir, "counters.json"), perm: 0o644},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := WriteFile(tt.path, []byte("first"), tt.perm); err != nil {
				t.Fatalf("first write: %v", err)
			}
			if got, err := os.ReadFile(tt.path); err != nil || string(got) != "first" {
				t.Fatalf("first write read back %q, err %v", got, err)
			}
			if st, err := os.Stat(tt.path); err != nil {
				t.Fatalf("stat: %v", err)
			} else if got := st.Mode().Perm(); got != tt.perm {
				t.Fatalf("mode = %o, want %o", got, tt.perm)
			}
			if err := WriteFile(tt.path, []byte("second"), tt.perm); err != nil {
				t.Fatalf("second write: %v", err)
			}
			if got, err := os.ReadFile(tt.path); err != nil || string(got) != "second" {
				t.Fatalf("second write read back %q, err %v", got, err)
			}
			rest, err := os.ReadDir(filepath.Dir(tt.path))
			if err != nil {
				t.Fatalf("read dir: %v", err)
			}
			dot := "." + filepath.Base(tt.path)
			for _, e := range rest {
				if e.Name() != filepath.Base(tt.path) && strings.HasPrefix(e.Name(), dot) {
					t.Errorf("temp file left behind: %s", e.Name())
				}
			}
		})
	}
}
