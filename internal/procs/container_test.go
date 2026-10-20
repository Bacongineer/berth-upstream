// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

// The cgroup shapes the container reading recognises, as docker, podman and
// containerd write them, and the shapes it must refuse.

import (
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
