// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package vfio

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func (r Reader) sysfs() string {
	if r.Sysfs == "" {
		return "/sys"
	}
	return r.Sysfs
}

func (r Reader) proc() string {
	if r.Proc == "" {
		return "/proc"
	}
	return r.Proc
}

// Supported is nil on Linux, where the reader's shapes exist.
func (r Reader) Supported() error { return nil }

// IOMMUOn reports whether the kernel exposes IOMMU groups, which VFIO
// needs: /sys/kernel/iommu_groups has one entry per group when VT-d or
// AMD-Vi is on (iommu=on iommu=pt, or the platform's own form).
func (r Reader) IOMMUOn() (bool, error) {
	entries, err := os.ReadDir(filepath.Join(r.sysfs(), "kernel", "iommu_groups"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read the IOMMU groups: %w", err)
	}
	return len(entries) > 0, nil
}

// Group reads the card's IOMMU group: the functions in it, each with the
// driver bound to it now, sorted by address.
func (r Reader) Group(addr string) ([]Function, error) {
	dir := filepath.Join(r.sysfs(), "bus", "pci", "devices", addr, "iommu_group", "devices")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read the IOMMU group of %s: %w", addr, err)
	}
	fs := make([]Function, 0, len(entries))
	for _, e := range entries {
		d, err := r.Driver(e.Name())
		if err != nil {
			return nil, err
		}
		fs = append(fs, Function{Addr: e.Name(), Driver: d})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].Addr < fs[j].Addr })
	return fs, nil
}

// Driver is the driver bound to one PCI function: the basename of its
// driver symlink, "" when none is bound.
func (r Reader) Driver(addr string) (string, error) {
	t, err := os.Readlink(filepath.Join(r.sysfs(), "bus", "pci", "devices", addr, "driver"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the driver of %s: %w", addr, err)
	}
	return filepath.Base(t), nil
}

// Openers lists the processes holding a device node of the card open, one
// entry per process, sorted by PID: the card's own /dev/nvidiaN, its DRM
// nodes under /dev/dri, and the shared /dev/nvidia* nodes (nvidiactl,
// nvidia-uvm) of a process that holds no other card's node. An engine on
// another card keeps the shared nodes open and does not hold this one.
// When the card's minor cannot be read, every /dev/nvidia* counts. Berth's
// own probe (an nvidia-smi under the berth process) is named as such, so
// the vfio commands can tell it apart from a real holder.
func (r Reader) Openers(addr string) ([]Opener, error) {
	drm, err := r.drmNodes(addr)
	if err != nil {
		return nil, err
	}
	minor := r.minor(addr)
	entries, err := os.ReadDir(r.proc())
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", r.proc(), err)
	}
	var out []Opener
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		// A process that exited between the listing and now has no fd
		// directory left: it holds nothing and is not listed.
		if _, err := os.Stat(filepath.Join(r.proc(), e.Name())); err != nil {
			continue
		}
		// A kernel thread (a readable, empty cmdline) holds no files; its fd
		// directory is root's alone, so reading it would only add noise.
		if b, err := os.ReadFile(filepath.Join(r.proc(), e.Name(), "cmdline")); err == nil && len(b) == 0 {
			continue
		}
		fds := filepath.Join(r.proc(), e.Name(), "fd")
		list, err := os.ReadDir(fds)
		if err != nil {
			// Permission or a process that died mid-walk: listed as
			// unreadable, never skipped.
			out = append(out, Opener{PID: pid, Name: r.comm(pid), Unreadable: true})
			continue
		}
		var nodes []string
		own, other := false, false
		for _, f := range list {
			t, err := os.Readlink(filepath.Join(fds, f.Name()))
			if err != nil {
				continue // the fd closed between listing and reading
			}
			k := nodeOf(t, minor, drm)
			own = own || k == nodeOwn
			other = other || k == nodeOther
			// One node opened many times reads once.
			if (k == nodeOwn || k == nodeShared) && !slices.Contains(nodes, t) {
				nodes = append(nodes, t)
			}
		}
		if !own && (other || len(nodes) == 0) {
			continue
		}
		out = append(out, Opener{PID: pid, Name: r.comm(pid), Nodes: nodes, Probe: r.isProbe(pid)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// drmNodes is the card's DRM node names under /dev/dri, from the device's
// drm directory (card0, renderD128).
func (r Reader) drmNodes(addr string) (map[string]bool, error) {
	dir := filepath.Join(r.sysfs(), "bus", "pci", "devices", addr, "drm")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the DRM nodes of %s: %w", addr, err)
	}
	nodes := make(map[string]bool, len(entries))
	for _, e := range entries {
		nodes[e.Name()] = true
	}
	return nodes, nil
}

// node is what an open fd's target is to the card being switched.
type node int

const (
	nodeNone   node = iota
	nodeOwn         // the card's /dev/nvidiaN or one of its DRM nodes
	nodeOther       // another card's /dev/nvidiaM
	nodeShared      // nvidiactl, nvidia-uvm, nvidia-modeset, nvidia-caps
)

// nodeOf sorts one fd target. minor is the card's /dev/nvidiaN number, -1
// when unknown, and then every /dev/nvidia* is the card's own.
func nodeOf(target string, minor int, drm map[string]bool) node {
	if filepath.Dir(target) == "/dev/dri" && drm[filepath.Base(target)] {
		return nodeOwn
	}
	rest, ok := strings.CutPrefix(target, "/dev/nvidia")
	switch {
	case !ok:
		return nodeNone
	case minor < 0:
		return nodeOwn
	}
	n, err := strconv.Atoi(rest)
	switch {
	case err != nil:
		return nodeShared
	case n == minor:
		return nodeOwn
	}
	return nodeOther
}

// minor is the card's /dev/nvidiaN number, from the NVIDIA driver's
// information file for its PCI address; -1 when the driver does not list
// the card (it is on another driver) or the file cannot be read.
func (r Reader) minor(addr string) int {
	b, err := os.ReadFile(filepath.Join(r.proc(), "driver", "nvidia", "gpus", addr, "information"))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "Device Minor:"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return -1
}

// comm is the process's command name; empty when unreadable.
func (r Reader) comm(pid int) string {
	b, err := os.ReadFile(filepath.Join(r.proc(), strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// isProbe reports whether the process is berthd's own GPU probe: an
// nvidia-smi running under the berth process.
func (r Reader) isProbe(pid int) bool {
	if r.comm(pid) != "nvidia-smi" {
		return false
	}
	p := pid
	for i := 0; i < 64; i++ {
		parent, ok := r.parent(p)
		if !ok {
			return false
		}
		if r.comm(parent) == "berth" {
			return true
		}
		p = parent
	}
	return false
}

// parent is the process's parent PID from /proc/<pid>/stat.
func (r Reader) parent(pid int) (int, bool) {
	b, err := os.ReadFile(filepath.Join(r.proc(), strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
