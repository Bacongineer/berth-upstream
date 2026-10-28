// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package procs answers one question: is this process a descendant of that
// one? Berth uses it to attribute a GPU process to the lease whose wrapper
// started it, even when the engine forks (Strata's server.py spawns the
// engine binary that actually holds the memory).
package procs

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Tree looks up a process's parent and start time. A PID alone is not an
// identity: the kernel reuses PIDs, so ownership is always PID plus start time.
type Tree interface {
	// Parent returns the parent PID, or 0 if the process is gone or unknown.
	Parent(pid int) int
	// StartTime returns when the process started, in an opaque monotonic unit,
	// or 0 if it is gone or the platform cannot tell.
	StartTime(pid int) uint64
}

// DescendsFrom reports whether pid is root or one of root's descendants.
func DescendsFrom(t Tree, pid, root int) bool {
	if root <= 0 {
		return false
	}
	for depth := 0; pid > 1 && depth < 64; depth++ {
		if pid == root {
			return true
		}
		pid = t.Parent(pid)
	}
	return false
}

// Map is a fixed parent table with no start times, for tests.
type Map map[int]int

func (m Map) Parent(pid int) int       { return m[pid] }
func (m Map) StartTime(pid int) uint64 { return 0 }

// Fake is a parent and start-time table, for tests that reuse PIDs.
type Fake struct {
	Parents map[int]int
	Starts  map[int]uint64
}

func (f Fake) Parent(pid int) int       { return f.Parents[pid] }
func (f Fake) StartTime(pid int) uint64 { return f.Starts[pid] }

// Proc is one process as List reports it: identity is PID plus start time,
// and the parent PID is what attributes descendants to a lease.
type Proc struct {
	PID    int
	Parent int
	Start  uint64
	Name   string
}

// containerID parses a cgroups file (v1, v2 or a mix: each line is
// controllers:id:path) and returns the container id of the engine runtime
// that owns the process, "" when it finds none. Berth reads only the
// runtime CLI's shapes: docker, podman and containerd/CRI, plus Proxmox
// LXC containers, whose id is typed lxc:<vmid>. A conmon scope is podman's
// supervisor for a container, and lxc.monitor is LXC's, not a container.
func containerID(r io.Reader) (string, bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		if id, ok := containerLine(parts[2]); ok {
			return id, true
		}
	}
	return "", false
}

// containerLine reads the path part of one cgroup line: a container id back
// when the path names one of the known runtime shapes. It scans from the
// innermost segment out, so a Docker container inside a Proxmox LXC is the
// Docker container.
func containerLine(path string) (string, bool) {
	segs := strings.Split(strings.TrimSpace(path), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		seg := strings.TrimSuffix(segs[i], ".scope")
		switch {
		case seg == "libpod-conmon", strings.HasPrefix(seg, "libpod-conmon-"),
			seg == "lxc.monitor":
			return "", false
		}
		for _, pre := range []string{"docker-", "cri-containerd-", "libpod-"} {
			if strings.HasPrefix(seg, pre) && hex64(seg[len(pre):]) {
				return seg[len(pre):], true
			}
		}
		// v1 docker: the container is a bare id directory named by the
		// parent segment.
		if i > 0 && segs[i-1] == "docker" && hex64(seg) {
			return seg, true
		}
		// Proxmox LXC: an lxc segment, then the container's numeric VMID
		// (/lxc/100/ns/init.scope on PVE 9, cgroup v2).
		if i > 0 && segs[i-1] == "lxc" && vmid(seg) {
			return lxcPrefix + seg, true
		}
	}
	return "", false
}

// lxcPrefix types a Proxmox LXC container id: lxc:<vmid>. No runtime CLI
// knows the id, so the name lookup reads Proxmox's config for it instead.
const lxcPrefix = "lxc:"

// vmid is a Proxmox guest id: decimal digits only.
func vmid(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// pveLXCDir holds each Proxmox LXC container's config, <vmid>.conf.
const pveLXCDir = "/etc/pve/lxc"

// LXCName names a Proxmox LXC container id (lxc:<vmid>) by the hostname in
// /etc/pve/lxc/<vmid>.conf. ok is false for any other id, which the caller
// resolves with a runtime CLI instead. Best effort: the file is root:www-data
// mode 640, so a berthd running as a normal user reads "" and the report
// shows the id alone.
func LXCName(id string) (name string, ok bool) { return lxcName(pveLXCDir, id) }

func lxcName(dir, id string) (string, bool) {
	n, ok := strings.CutPrefix(id, lxcPrefix)
	if !ok || !vmid(n) {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, n+".conf"))
	if err != nil {
		return "", true
	}
	return lxcHostname(bytes.NewReader(b)), true
}

// lxcHostname reads the hostname key of a Proxmox container config, ""
// when it has none. Keys after the first [section] belong to a snapshot or
// a pending change, not to the container as it runs.
func lxcHostname(r io.Reader) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			break
		}
		key, val, found := strings.Cut(line, ":")
		if found && strings.TrimSpace(key) == "hostname" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

// hex64 is a 64-character hex container id.
func hex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// scopeID parses a cgroups file and returns the path of the nearest
// ancestor cgroup named berth-*.scope, the scope up to and including
// that segment, "" when there is none. Only the cgroup v2 unified line
// ("0::/path") counts: Berth creates scopes on v2 and reads them on v2.
func scopeID(r io.Reader) (string, bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "0::") {
			continue
		}
		if p, ok := scopePath(strings.TrimPrefix(line, "0::")); ok {
			return p, true
		}
	}
	return "", false
}

// scopePath reads one v2 cgroup path: the prefix up to and including
// the deepest berth-*.scope segment, so every process nested further
// under the same scope answers with the same path.
func scopePath(path string) (string, bool) {
	segs := strings.Split(strings.TrimSpace(path), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if berthScope(segs[i]) {
			return strings.Join(segs[:i+1], "/"), true
		}
	}
	return "", false
}

// berthScope is a Berth scope unit name: berth-, something, .scope.
func berthScope(seg string) bool {
	return strings.HasPrefix(seg, "berth-") && strings.HasSuffix(seg, ".scope") &&
		len(seg) > len("berth-.scope")
}

// Mem is the memory a cgroup's memory.stat charges to it, in bytes. Anon
// and Shmem stay held until the processes exit; File is page cache, which
// the kernel already counts as available and reclaims on its own.
type Mem struct {
	Anon, File, Shmem uint64
}

// scopeMem parses a cgroup v2 memory.stat file. ok is false when it holds
// no anon line, so a file that is not memory.stat never reads as zero.
func scopeMem(r io.Reader) (Mem, bool) {
	var m Mem
	ok := false
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, found := strings.Cut(sc.Text(), " ")
		if !found {
			continue
		}
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "anon":
			m.Anon, ok = n, true
		case "file":
			m.File = n
		case "shmem":
			m.Shmem = n
		}
	}
	if !ok {
		return Mem{}, false
	}
	return m, true
}
