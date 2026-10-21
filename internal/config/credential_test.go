// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateCredentialSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("fixture-file-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := Engine{APIKeyFile: path}
	if key, err := e.APIKey(); err != nil || key != "fixture-file-token" {
		t.Fatalf("private file: key match=%v err=%v", key == "fixture-file-token", err)
	}
	t.Setenv("FIXTURE_CREDENTIAL", "fixture-env-token")
	e.APIKeyEnv = "FIXTURE_CREDENTIAL"
	if _, err := e.APIKey(); err == nil {
		t.Fatal("accepted ambiguous credential sources")
	}
	e.APIKeyEnv = ""
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	e.APIKeyFile = link
	if _, err := e.APIKey(); err == nil {
		t.Fatal("accepted a symlink")
	}
	e.APIKeyFile = path
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.APIKey(); err == nil {
		t.Fatal("accepted a shared credential file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "first\nsecond", "two tokens", "one\ttwo"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := e.APIKey(); err == nil {
			t.Fatalf("accepted invalid token shape %q", body)
		}
	}
	e.APIKeyFile = "relative-key"
	if _, err := e.APIKey(); err == nil {
		t.Fatal("accepted relative credential path")
	}
}
