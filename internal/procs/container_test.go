// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

// The cgroup shapes the container reading recognises, as docker, podman and
// containerd write them, and the shapes it must refuse.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContainerLine(t *testing.T) {
	const id = "9b5f6c4d3e2a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f"
	tests := []struct {
		name string
		path string
		id   string
		ok   bool
	}{
		{"docker cgroup v2 scope", "/system.slice/docker-" + id + ".scope", id, true},
		{"docker cgroup v1 directory", "/docker/" + id, id, true},
		{"docker cgroup v1 deep", "/system.slice/docker.service/../docker/" + id, id, true},
		{"podman cgroup v2 scope", "/user.slice/user-1000.slice/user@1000.service/libpod-" + id + ".scope", id, true},
		{"podman cgroup v1", "/libpod_parent/libpod-" + id + ".scope", id, true},
		{"containerd CRI scope", "/kubepods/besteffort/pod12345/cri-containerd-" + id + ".scope", id, true},
		{"containerd plain scope", "/system.slice/cri-containerd-" + id + ".scope", id, true},
		{"podman conmon is not a container", "/libpod_parent/libpod-conmon-" + id + ".scope", "", false},
		{"docker scope with a name, not an id", "/system.slice/docker-vllm.scope", "", false},
		{"short id is not an id", "/docker/9b5f6c4d3e2a", "", false},
		{"non hex id", "/docker/0123456789abcdefg01234567890123456789012345678901234567890123456", "", false},
		{"host session scope", "/user.slice/user-1000.slice/session-2.scope", "", false},
		{"host service scope", "/system.slice/sddm.service", "", false},
		{"empty path", "", "", false},
		// Proxmox VE 9.2, cgroup v2: processes inside CT 100, and the
		// lxc-start monitor that supervises it.
		{"proxmox lxc init", "/lxc/100/ns/init.scope", "lxc:100", true},
		{"proxmox lxc service", "/lxc/100/ns/system.slice/cron.service", "lxc:100", true},
		{"proxmox lxc monitor is not a container", "/lxc.monitor/100", "", false},
		{"docker inside a proxmox lxc", "/lxc/100/ns/system.slice/docker-" + id + ".scope", id, true},
		{"lxc with a name, not a vmid", "/lxc/ollama/ns/init.scope", "", false},
		{"lxc segment alone", "/lxc", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := containerLine(tt.path)
			if id != tt.id || ok != tt.ok {
				t.Fatalf("containerLine(%q) = %q, %v, want %q, %v", tt.path, id, ok, tt.id, tt.ok)
			}
		})
	}
}

func TestContainerID(t *testing.T) {
	const id = "9b5f6c4d3e2a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f"
	other := strings.Repeat("a", 64)
	tests := []struct {
		name string
		body string
		id   string
		ok   bool
	}{
		{"docker v2 with controllers above", "0::/init.scope\n0::/system.slice/docker-" + id + ".scope\n", id, true},
		{"docker v1 repeats across controllers",
			"12:blkio:/docker/" + id + "\n11:devices:/docker/" + id + "\n",
			id, true},
		{"conmon line then container line", "0::/libpod-conmon-" + other + ".scope\n0::/libpod-" + id + ".scope\n", id, true},
		{"conmon only", "0::/libpod-conmon-" + id + ".scope\n", "", false},
		{"proxmox lxc init", "0::/lxc/100/ns/init.scope\n", "lxc:100", true},
		{"proxmox lxc service", "0::/lxc/100/ns/system.slice/cron.service\n", "lxc:100", true},
		{"proxmox lxc monitor", "0::/lxc.monitor/100\n", "", false},
		{"no cgroup paths at all", "not a cgroup file\n", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, ok := containerID(strings.NewReader(tt.body))
			if id != tt.id || ok != tt.ok {
				t.Fatalf("containerID = %q, %v, want %q, %v", id, ok, tt.id, tt.ok)
			}
		})
	}
}

// A Proxmox LXC container is named by the hostname in its config, read
// best effort: Proxmox keeps the file root:www-data mode 640.
func TestLXCName(t *testing.T) {
	dir := t.TempDir()
	conf := "#GPU host for inference\narch: amd64\ncores: 8\nhostname: ollama\nmemory: 16384\n" +
		"lxc.mount.entry: /dev/nvidia0 dev/nvidia0 none bind,optional,create=file\n" +
		"\n[before-upgrade]\nhostname: ollama-old\n"
	if err := os.WriteFile(filepath.Join(dir, "100.conf"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "102.conf"), []byte("arch: amd64\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		id   string
		want string
		ok   bool
	}{
		{"hostname from the config", "lxc:100", "ollama", true},
		{"config it cannot read names nothing", "lxc:101", "", true},
		{"config without a hostname", "lxc:102", "", true},
		{"docker id is not an lxc id", strings.Repeat("a", 64), "", false},
		{"lxc prefix without a vmid", "lxc:", "", false},
		{"lxc prefix with a path", "lxc:../100", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := lxcName(dir, tt.id)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("lxcName(%q) = %q, %v, want %q, %v", tt.id, got, ok, tt.want, tt.ok)
			}
		})
	}
}
