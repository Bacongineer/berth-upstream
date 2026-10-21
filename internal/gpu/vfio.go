// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

// Reading the kernel driver a PCI function is bound to. This is the
// read-only half of the VFIO story: while the card is in a VM the probe
// cannot answer, and the driver binding is what says so. Nothing here
// writes to sysfs, unbinds a driver or loads a module.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// SysfsRoot is where the PCI tree is mounted. Tests point it at a temp dir.
var SysfsRoot = "/sys"

// VFIODriver is the kernel driver a VM passthrough binds the card to.
const VFIODriver = "vfio-pci"

// DriverFor is the kernel driver bound to the PCI function named by addr
// ("0000:01:00.0"): the basename of its driver symlink. "" when the
// function is unbound, the address is unknown, the tree is unreadable, or
// the machine is not Linux.
func DriverFor(addr string) string {
	if runtime.GOOS != "linux" || addr == "" {
		return ""
	}
	dst, err := os.Readlink(filepath.Join(SysfsRoot, "bus", "pci", "devices", addr, "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(dst)
}

// SysfsAddr turns nvidia-smi's bus id ("00000000:01:00.0") into the device
// name sysfs uses ("0000:01:00.0"): the domain is read as hex and printed
// at least four digits wide, and the rest is lowercased, as the kernel
// names devices. "" when the bus id is not a PCI address with a domain.
func SysfsAddr(busID string) string {
	dom, rest, ok := strings.Cut(busID, ":")
	if !ok {
		return ""
	}
	d, err := strconv.ParseUint(dom, 16, 32)
	if err != nil {
		return ""
	}
	bus, tail, ok := strings.Cut(rest, ":")
	if !ok {
		return ""
	}
	dev, fn, ok := strings.Cut(tail, ".")
	if !ok {
		return ""
	}
	for _, part := range []string{bus, dev, fn} {
		if _, err := strconv.ParseUint(part, 16, 32); err != nil {
			return ""
		}
	}
	return fmt.Sprintf("%04x:%s", d, strings.ToLower(rest))
}

// FindVFIOCard names the one PCI display function bound to vfio-pci, by its
// sysfs device name: vendor 0x10de (NVIDIA) and class 0x03xxxx (display).
// "" when none or several match, so a guess never decides. Off Linux: "".
func FindVFIOCard() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	devs, err := os.ReadDir(filepath.Join(SysfsRoot, "bus", "pci", "devices"))
	if err != nil {
		return ""
	}
	found := ""
	for _, d := range devs {
		addr := d.Name()
		base := filepath.Join(SysfsRoot, "bus", "pci", "devices", addr)
		if DriverFor(addr) != VFIODriver {
			continue
		}
		vendor, err := sysfsHex(filepath.Join(base, "vendor"))
		if err != nil || vendor != 0x10de {
			continue
		}
		class, err := sysfsHex(filepath.Join(base, "class"))
		if err != nil || class&0xFF0000 != 0x030000 {
			continue
		}
		if found != "" {
			return ""
		}
		found = addr
	}
	return found
}

// sysfsHex reads one sysfs attribute like "0x030000" as a number.
func sysfsHex(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 0, 32)
}
