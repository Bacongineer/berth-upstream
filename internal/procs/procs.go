// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package procs answers one question: is this process a descendant of that
// one? Berth uses it to attribute a GPU process to the lease whose wrapper
// started it, even when the engine forks (Strata's server.py spawns the
// engine binary that actually holds the memory).
package procs

import (
	"bufio"
	"io"
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
// runtime CLI's shapes: docker, podman and containerd/CRI. A conmon scope
// is podman's supervisor for a container, not a container.
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
// when the path names one of the known runtime shapes.
func containerLine(path string) (string, bool) {
	segs := strings.Split(strings.TrimSpace(path), "/")
	for i := len(segs) - 1; i >= 0; i-- {
		seg := strings.TrimSuffix(segs[i], ".scope")
		switch {
		case seg == "libpod-conmon", strings.HasPrefix(seg, "libpod-conmon-"):
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
	}
	return "", false
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
