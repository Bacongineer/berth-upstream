// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package host

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// GlobalMemoryStatusEx is not in golang.org/x/sys, so it is loaded by
// name.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx is MEMORYSTATUSEX (sysinfoapi.h), what
// GlobalMemoryStatusEx fills in.
type memoryStatusEx struct {
	Length     uint32
	MemoryLoad uint32
	TotalPhys  uint64
	AvailPhys  uint64
	// TotalPageFile is the system's commit limit: physical memory plus
	// the page file. AvailPageFile is what this process could still
	// commit, which is never more than the system-wide remainder.
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// globalMemoryStatus asks kernel32 once. ok is false when the call
// fails, which a caller reports as unsupported rather than idle.
func globalMemoryStatus() (memoryStatusEx, bool) {
	var m memoryStatusEx
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return memoryStatusEx{}, false
	}
	return m, true
}

// MemAvailable is GlobalMemoryStatusEx's available physical memory in
// bytes: the standby, free and zero pages the kernel can hand out
// without writing anything to disk first. It revises as the system
// reclaims pages, so it is never a reservation. The root is there to
// match the /proc reading off Linux and is ignored here.
func MemAvailable(root string) uint64 {
	m, ok := globalMemoryStatus()
	if !ok {
		return 0
	}
	return m.AvailPhys
}

// MemPressure stays unsupported on Windows: the kernel's memory pressure
// stall figures (PSI) are a Linux thing, and no Windows interface reads
// an equivalent.
func MemPressure(root string) (float64, bool) {
	return 0, false
}

// Read reports RAM and the page file from GlobalMemoryStatusEx and the
// core count from the runtime. CPU and the load averages have no Windows
// reading yet, so they stay 0; Cores is always the real one.
func (r *Reader) Read() Stats {
	m, ok := globalMemoryStatus()
	if !ok {
		return Stats{}
	}
	s := Stats{Supported: true, Cores: runtime.NumCPU()}
	s.RAMTotal = m.TotalPhys
	if m.AvailPhys <= m.TotalPhys {
		s.RAMUsed = m.TotalPhys - m.AvailPhys
	}
	s.SwapTotal = m.TotalPageFile
	if m.AvailPageFile <= m.TotalPageFile {
		s.SwapUsed = m.TotalPageFile - m.AvailPageFile
	}
	return s
}
