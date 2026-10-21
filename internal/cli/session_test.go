// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/runner"
)

func TestRunSessionFlags(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{"completed job", []string{"--exit-comfyui-idle", "--drain-comfyui", "http://127.0.0.1:1"}, true},
		{"unused deadline", []string{"--exit-comfyui-idle", "--exit-comfyui-idle-after", "1m", "--drain-comfyui", "http://127.0.0.1:1"}, true},
		{"missing queue", []string{"--exit-comfyui-idle"}, false},
		{"keep server", []string{"--exit-comfyui-idle", "--drain-comfyui", "http://127.0.0.1:1", "--keep-comfyui-up"}, false},
		{"deadline without session", []string{"--exit-comfyui-idle-after", "1m"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			args := []string{"run", "--as", "fixture", "--socket", filepath.Join(t.TempDir(), "missing.sock"), "--fail-closed"}
			args = append(args, tc.args...)
			args = append(args, "--", "true")
			code, _, stderr := runCLI(t, args...)
			if tc.valid {
				if code != runner.ExitRefused || !strings.Contains(stderr, "not starting fixture") {
					t.Fatalf("parsed run did not reach admission: %d: %s", code, stderr)
				}
			} else if code == 0 || !strings.Contains(stderr, "exit_comfyui_idle") {
				t.Fatalf("invalid session accepted: %d: %s", code, stderr)
			}
		})
	}
}
