// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package vfio reads the VFIO state of one PCI card: whether IOMMU is on,
// the card's IOMMU group, the driver bound to each function, and the
// processes holding the card's device nodes open. Berth reads this state to
// coordinate a passthrough; it never writes sysfs, never binds or unbinds a
// driver, and never stops a process.
//
// The reader's roots are parameters, so a test points it at a fake tree.
package vfio

import "strings"

// Reader reads the card's VFIO state from the machine's sysfs and /proc.
// Empty fields mean the real roots; tests name their own.
type Reader struct {
	Sysfs string
	Proc  string
}

// Function is one PCI function of the card's IOMMU group and the driver
// bound to it right now.
type Function struct {
	Addr   string `json:"addr"`             // 0000:01:00.0
	Driver string `json:"driver,omitempty"` // nvidia, vfio-pci, ...; "" when none is bound
}

// Opener is one process holding a device node of the card open, read from
// /proc/<pid>/fd. A process whose fd directory cannot be read is listed as
// unreadable, never skipped: the reader cannot prove it holds nothing.
type Opener struct {
	PID        int      `json:"pid"`
	Name       string   `json:"name,omitempty"`       // the command name, from /proc/<pid>/comm
	Nodes      []string `json:"nodes,omitempty"`      // /dev/nvidia0, /dev/dri/renderD128
	Probe      bool     `json:"probe,omitempty"`      // berthd's own nvidia-smi probe
	Unreadable bool     `json:"unreadable,omitempty"` // /proc/<pid>/fd could not be read
}

// SysAddr turns nvidia-smi's pci.bus_id form ("00000000:01:00.0") into the
// form sysfs uses for the same function ("0000:01:00.0").
func SysAddr(busID string) string {
	busID = strings.TrimSpace(busID)
	domain, fn, ok := strings.Cut(busID, ":")
	if !ok {
		return busID
	}
	domain = strings.TrimLeft(domain, "0")
	for len(domain) < 4 {
		domain = "0" + domain
	}
	return domain + ":" + fn
}

// CardFunction reports whether addr is one of the card's own functions: it
// shares the card's bus and device (0000:01:00.1 with 0000:01:00.0), so the
// whole card passes through. Any other function in the group is another
// device, and a group that holds one blocks the passthrough.
func CardFunction(addr, card string) bool {
	i, j := strings.LastIndexByte(addr, '.'), strings.LastIndexByte(card, '.')
	return i >= 0 && j >= 0 && addr[:i] == card[:j]
}
