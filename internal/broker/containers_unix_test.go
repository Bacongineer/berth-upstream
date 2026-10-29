// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package broker

import (
	"os"
	"path/filepath"
	"testing"
)

// A Proxmox LXC id is named from Proxmox's config, never by docker or
// podman: neither knows it, and either would only spend the 5 s deadline.
func TestLXCNameNeverAsksARuntime(t *testing.T) {
	dir := t.TempDir()
	asked := filepath.Join(dir, "asked")
	for _, rt := range []string{"docker", "podman"} {
		script := "#!/bin/sh\necho \"$*\" >> " + asked + "\necho /from-" + rt + "\n"
		if err := os.WriteFile(filepath.Join(dir, rt), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	if got := containerNameCLI("lxc:100"); got == "from-docker" || got == "from-podman" {
		t.Fatalf("containerNameCLI(lxc:100) = %q, want no runtime asked", got)
	}
	if _, err := os.Stat(asked); err == nil {
		b, _ := os.ReadFile(asked)
		t.Fatalf("a runtime was asked about an LXC id: %s", b)
	}
	// A runtime id still goes to the runtime.
	if got := containerNameCLI(vllmID); got != "from-docker" {
		t.Fatalf("containerNameCLI(docker id) = %q, want docker's answer", got)
	}
}
