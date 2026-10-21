// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package vfio

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// tree is a fake sysfs and /proc for one card at 0000:01:00.0.
type tree struct {
	t     *testing.T
	sysfs string
	proc  string
}

func newTree(t *testing.T) tree {
	t.Helper()
	return tree{t: t, sysfs: t.TempDir(), proc: t.TempDir()}
}

func (tr tree) reader() Reader { return Reader{Sysfs: tr.sysfs, Proc: tr.proc} }

// device makes one PCI function with a driver ("" = unbound) and links it
// into the card's IOMMU group, the way sysfs lists the group's members.
func (tr tree) device(addr, driver string) {
	tr.t.Helper()
	d := filepath.Join(tr.sysfs, "bus", "pci", "devices", addr)
	if err := os.MkdirAll(d, 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if driver != "" {
		if err := os.Symlink(filepath.Join(tr.sysfs, "bus", "pci", "drivers", driver), filepath.Join(d, "driver")); err != nil {
			tr.t.Fatal(err)
		}
	}
	group := filepath.Join(tr.sysfs, "kernel", "iommu_groups", "12")
	if err := os.MkdirAll(filepath.Join(group, "devices"), 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.Symlink(group, filepath.Join(d, "iommu_group")); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.Symlink(d, filepath.Join(group, "devices", addr)); err != nil {
		tr.t.Fatal(err)
	}
}

// drm adds the card's DRM node names to sysfs.
func (tr tree) drm(names ...string) {
	tr.t.Helper()
	dir := filepath.Join(tr.sysfs, "bus", "pci", "devices", cardAddr, "drm")
	for _, n := range names {
		if err := os.MkdirAll(filepath.Join(dir, n), 0o755); err != nil {
			tr.t.Fatal(err)
		}
	}
}

// process makes one /proc entry: a command name and a parent, and fd links
// for every node given.
func (tr tree) process(pid int, comm string, parent int, nodes ...string) {
	tr.t.Helper()
	p := filepath.Join(tr.proc, fmt.Sprintf("%d", pid))
	if err := os.MkdirAll(p, 0o755); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "comm"), []byte(comm+"\n"), 0o644); err != nil {
		tr.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "stat"), []byte(fmt.Sprintf("%d (%s) S %d 0 0 0 0", pid, comm, parent)), 0o644); err != nil {
		tr.t.Fatal(err)
	}
	fds := filepath.Join(p, "fd")
	if err := os.MkdirAll(fds, 0o755); err != nil {
		tr.t.Fatal(err)
	}
	for i, n := range nodes {
		if err := os.Symlink(n, filepath.Join(fds, fmt.Sprintf("%d", i))); err != nil {
			tr.t.Fatal(err)
		}
	}
}

// locked closes one process's fd directory to everyone.
func (tr tree) locked(pid int) {
	tr.t.Helper()
	if err := os.Chmod(filepath.Join(tr.proc, fmt.Sprintf("%d", pid), "fd"), 0o000); err != nil {
		tr.t.Fatal(err)
	}
}

const cardAddr = "0000:01:00.0"

// TestSysAddrConvertsNvidiaSmisForm covers the bus ids nvidia-smi prints.
func TestSysAddrConvertsNvidiaSmisForm(t *testing.T) {
	cases := []struct{ in, want string }{
		{"00000000:01:00.0", "0000:01:00.0"},
		{"00000000:81:00.0", "0000:81:00.0"},
		{"0000:01:00.0", "0000:01:00.0"},
		{"  00000000:01:00.0  ", "0000:01:00.0"},
	}
	for _, tc := range cases {
		if got := SysAddr(tc.in); got != tc.want {
			t.Errorf("SysAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCardFunctionSplitsTheCardFromItsGroup covers the rule that decides a
// passthrough blocker.
func TestCardFunctionSplitsTheCardFromItsGroup(t *testing.T) {
	cases := []struct {
		addr, card string
		want       bool
	}{
		{"0000:01:00.0", "0000:01:00.0", true},
		{"0000:01:00.1", "0000:01:00.0", true},  // the card's audio function
		{"0000:06:00.0", "0000:01:00.0", false}, // another device in the group
		{"0000:02:00.0", "0000:01:00.0", false}, // another bus
		{"", "0000:01:00.0", false},
	}
	for _, tc := range cases {
		if got := CardFunction(tc.addr, tc.card); got != tc.want {
			t.Errorf("CardFunction(%q, %q) = %v, want %v", tc.addr, tc.card, got, tc.want)
		}
	}
}

// TestIOMMUOnReadsTheKernelGroups: no groups means off, entries mean on.
func TestIOMMUOnReadsTheKernelGroups(t *testing.T) {
	t.Run("off when the kernel has no groups", func(t *testing.T) {
		on, err := Reader{Sysfs: t.TempDir()}.IOMMUOn()
		if err != nil || on {
			t.Fatalf("IOMMUOn() = %v, %v", on, err)
		}
	})
	t.Run("on when the kernel has groups", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "kernel", "iommu_groups", "12"), 0o755); err != nil {
			t.Fatal(err)
		}
		on, err := Reader{Sysfs: root}.IOMMUOn()
		if err != nil || !on {
			t.Fatalf("IOMMUOn() = %v, %v", on, err)
		}
	})
}

// TestGroupReadsMembersAndTheirDrivers: the group lists every function and
// the driver each is bound to, unbound reads "", missing group errors.
func TestGroupReadsMembersAndTheirDrivers(t *testing.T) {
	tt := newTree(t)
	tt.device(cardAddr, "nvidia")
	tt.device("0000:01:00.1", "snd_hda_intel")
	tt.device("0000:06:00.0", "")

	g, err := tt.reader().Group(cardAddr)
	if err != nil {
		t.Fatal(err)
	}
	want := []Function{
		{Addr: cardAddr, Driver: "nvidia"},
		{Addr: "0000:01:00.1", Driver: "snd_hda_intel"},
		{Addr: "0000:06:00.0", Driver: ""},
	}
	if len(g) != len(want) {
		t.Fatalf("group = %+v, want %+v", g, want)
	}
	for i, f := range g {
		if f != want[i] {
			t.Errorf("group[%d] = %+v, want %+v", i, f, want[i])
		}
	}
	if _, err := tt.reader().Group("0000:09:00.0"); err == nil {
		t.Error("a card with no IOMMU group read without error")
	}
}

// TestDriverReadsBoundUnboundAndMissing covers the driver symlink's states.
func TestDriverReadsBoundUnboundAndMissing(t *testing.T) {
	tt := newTree(t)
	tt.device(cardAddr, "nvidia")
	r := tt.reader()
	if d, err := r.Driver(cardAddr); err != nil || d != "nvidia" {
		t.Fatalf("Driver = %q, %v", d, err)
	}
	if d, err := r.Driver("0000:01:00.1"); err != nil || d != "" {
		t.Fatalf("unbound Driver = %q, %v", d, err)
	}
	if d, err := r.Driver("0000:09:00.0"); err != nil || d != "" {
		t.Fatalf("missing device Driver = %q, %v", d, err)
	}
}

// TestOpenersFindsTheCardHoldersAndNamesBerthdsProbe: /dev/nvidia* and the
// card's DRM nodes are found, one entry per process, the nvidia-smi under
// the berth process is the probe, an unreadable fd directory is listed as
// unreadable, and an unrelated fd is not.
func TestOpenersFindsTheCardHoldersAndNamesBerthdsProbe(t *testing.T) {
	tt := newTree(t)
	tt.device(cardAddr, "nvidia")
	tt.drm("card1", "renderD128")
	tt.process(100, "app", 1, "/dev/nvidia0", "/dev/dri/renderD128")
	tt.process(101, "other", 1, "/dev/tty1") // not the card's
	tt.process(102, "nvidia-smi", 99, "/dev/nvidiactl")
	tt.process(104, "app", 1, "/dev/dri/card1")
	tt.process(103, "locked", 1)
	tt.locked(103)
	tt.process(99, "berth", 1)
	tt.process(98, "filler", 1)

	ops, err := tt.reader().Openers(cardAddr)
	if err != nil {
		t.Fatal(err)
	}
	want := []Opener{
		{PID: 100, Name: "app", Nodes: []string{"/dev/nvidia0", "/dev/dri/renderD128"}},
		{PID: 102, Name: "nvidia-smi", Nodes: []string{"/dev/nvidiactl"}, Probe: true},
		{PID: 103, Name: "locked", Unreadable: true},
		{PID: 104, Name: "app", Nodes: []string{"/dev/dri/card1"}},
	}
	if len(ops) != len(want) {
		t.Fatalf("openers = %+v, want %+v", ops, want)
	}
	for i, o := range ops {
		if o.PID != want[i].PID || o.Name != want[i].Name || o.Probe != want[i].Probe || o.Unreadable != want[i].Unreadable {
			t.Errorf("openers[%d] = %+v, want %+v", i, o, want[i])
		}
		if len(o.Nodes) != len(want[i].Nodes) {
			t.Errorf("openers[%d].Nodes = %v, want %v", i, o.Nodes, want[i].Nodes)
			continue
		}
		for j, n := range o.Nodes {
			if n != want[i].Nodes[j] {
				t.Errorf("openers[%d].Nodes[%d] = %q, want %q", i, j, n, want[i].Nodes[j])
			}
		}
	}
}

// TestOpenersWithoutDrm: a card whose drm directory is absent reads with no
// error and finds the nvidia holders.
func TestOpenersWithoutDrm(t *testing.T) {
	tt := newTree(t)
	tt.device(cardAddr, "nvidia")
	tt.process(100, "app", 1, "/dev/nvidia0")
	ops, err := tt.reader().Openers(cardAddr)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].PID != 100 || len(ops[0].Nodes) != 1 || ops[0].Nodes[0] != "/dev/nvidia0" {
		t.Fatalf("openers = %+v", ops)
	}
}
