// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionConfigPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"completed job", "drain_comfyui='http://127.0.0.1:1'\nexit_comfyui_idle=true", true},
		{"unused deadline", "drain_comfyui='http://127.0.0.1:1'\nexit_comfyui_idle=true\nexit_comfyui_idle_after='1m'", true},
		{"missing queue", "exit_comfyui_idle=true", false},
		{"keep server", "drain_comfyui='http://127.0.0.1:1'\nexit_comfyui_idle=true\nkeep_comfyui_up=true", false},
		{"deadline without session", "exit_comfyui_idle_after='1m'", false},
		{"negative deadline", "drain_comfyui='http://127.0.0.1:1'\nexit_comfyui_idle=true\nexit_comfyui_idle_after='-1m'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			text := "[[engine]]\nname='fixture'\ntype='comfyui'\nurl='http://127.0.0.1:1'\n" + tc.fields
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, warnings, err := Check(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.valid {
				if !cfg.Engine[0].ExitComfyUIIdle {
					t.Fatal("session flag lost")
				}
				for _, warning := range warnings {
					if warning.Key == "exit_comfyui_idle" || warning.Key == "exit_comfyui_idle_after" {
						t.Fatalf("unknown session key: %+v", warning)
					}
				}
			}
		})
	}
}
