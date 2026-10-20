// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package gpu

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The PDH API is not in golang.org/x/sys, so it is loaded by name. The
// explicit W entries are the Unicode forms; English counter names resolve
// on a localized install too.
var (
	pdh = windows.NewLazySystemDLL("pdh.dll")

	pdhOpenQuery                 = pdh.NewProc("PdhOpenQueryW")
	pdhAddEnglishCounterW        = pdh.NewProc("PdhAddEnglishCounterW")
	pdhCollectQueryData          = pdh.NewProc("PdhCollectQueryData")
	pdhGetFormattedCounterArrayW = pdh.NewProc("PdhGetFormattedCounterArrayW")
	pdhCloseQuery                = pdh.NewProc("PdhCloseQuery")
)

const (
	pdhFmtLarge = 0x00000400 // the counter formats as LONGLONG
	pdhMoreData = 0x800007D2 // the caller's buffer was too small
	pdhNoData   = 0x800007D5 // no sample yet, or no instances
	pdhSuccess  = 0
)

// gpuProcMemPath is the counter Task Manager's per-process GPU memory
// comes from, every instance at once.
const gpuProcMemPath = `\GPU Process Memory(*)\Dedicated Usage`

// counterValue mirrors PDH_FMT_COUNTERVALUE: a status code and the value
// union, read here as the LONGLONG arm.
type counterValue struct {
	CStatus uint32
	Large   int64
}

// counterItem mirrors PDH_FMT_COUNTERVALUE_ITEM_W.
type counterItem struct {
	Name     *uint16
	FmtValue counterValue
}

// gpuProcQuery is one open PDH query on gpuProcMemPath.
type gpuProcQuery struct {
	query, counter uintptr
}

// newGPUProcQuery opens the query and adds the English counter. An error
// here usually means the object is absent, as on a machine with no GPU
// or without the WDDM counters.
func newGPUProcQuery() (*gpuProcQuery, error) {
	for _, p := range []*windows.LazyProc{pdhOpenQuery, pdhAddEnglishCounterW, pdhCollectQueryData, pdhGetFormattedCounterArrayW, pdhCloseQuery} {
		if err := p.Find(); err != nil {
			return nil, err
		}
	}
	g := &gpuProcQuery{}
	if r, _, _ := pdhOpenQuery.Call(0, 0, uintptr(unsafe.Pointer(&g.query))); r != pdhSuccess {
		return nil, g.status("open", r)
	}
	path, err := windows.UTF16PtrFromString(gpuProcMemPath)
	if err != nil {
		g.close()
		return nil, err
	}
	if r, _, _ := pdhAddEnglishCounterW.Call(g.query, uintptr(unsafe.Pointer(path)), 0, uintptr(unsafe.Pointer(&g.counter))); r != pdhSuccess {
		g.close()
		return nil, g.status("add counter", r)
	}
	return g, nil
}

func (g *gpuProcQuery) close() {
	if g.query != 0 {
		_, _, _ = pdhCloseQuery.Call(g.query)
		g.query, g.counter = 0, 0
	}
}

func (gpuProcQuery) status(what string, code uintptr) error {
	return fmt.Errorf("pdh %s: 0x%08x", what, code)
}

// collect gathers one sample of every live instance.
func (g *gpuProcQuery) collect() error {
	if r, _, _ := pdhCollectQueryData.Call(g.query); r != pdhSuccess {
		return g.status("collect", r)
	}
	return nil
}

// dedicated formats the collected sample: one entry per instance.
func (g *gpuProcQuery) dedicated() ([]dedicatedUsage, error) {
	var size, count uint32
	r, _, _ := pdhGetFormattedCounterArrayW.Call(g.counter, pdhFmtLarge,
		uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)), 0)
	var out []dedicatedUsage
	for n := 0; r == pdhMoreData && n < 3; n++ {
		buf := make([]byte, size)
		r, _, _ = pdhGetFormattedCounterArrayW.Call(g.counter, pdhFmtLarge,
			uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&count)),
			uintptr(unsafe.Pointer(&buf[0])))
		if r == pdhSuccess {
			out = parseItems(buf, count)
		}
	}
	if r == pdhNoData {
		return nil, nil
	}
	if r != pdhSuccess {
		return nil, g.status("format", r)
	}
	return out, nil
}

// parseItems reads the formatted buffer as one entry per instance whose sample
// carries a usable positive figure.
func parseItems(buf []byte, count uint32) []dedicatedUsage {
	if count == 0 || len(buf) == 0 {
		return nil
	}
	slice := unsafe.Slice((*counterItem)(unsafe.Pointer(&buf[0])), count)
	out := make([]dedicatedUsage, 0, len(slice))
	for _, it := range slice {
		if it.FmtValue.CStatus != pdhSuccess || it.FmtValue.Large <= 0 || it.Name == nil {
			continue
		}
		out = append(out, dedicatedUsage{Instance: windows.UTF16PtrToString(it.Name), Bytes: uint64(it.FmtValue.Large)})
	}
	return out
}

var (
	pdhMu  sync.Mutex
	pdhRun *gpuProcQuery
)

// pdhDedicated reads every process's dedicated GPU memory. The query
// stays open between reads and samples fresh at each collect; when a
// read fails the query is closed so the next read opens it again.
func pdhDedicated() (map[int]uint64, error) {
	pdhMu.Lock()
	defer pdhMu.Unlock()
	if pdhRun == nil {
		g, err := newGPUProcQuery()
		if err != nil {
			return nil, err
		}
		pdhRun = g
	}
	if err := pdhRun.collect(); err != nil {
		pdhRun.close()
		pdhRun = nil
		return nil, err
	}
	found, err := pdhRun.dedicated()
	if err != nil {
		pdhRun.close()
		pdhRun = nil
		return nil, err
	}
	return dedicatedByPid(found), nil
}

// withPDH merges PDH's per-process reading into a parsed snapshot. The
// driver's totals stand; PDH is Windows' only view of a process's own
// VRAM under WDDM. A PDH failure, which happens on a machine with no GPU
// and can happen on one with a GPU, keeps the snapshot as nvidia-smi
// gave it and the next read retries.
func withPDH(s Snapshot) Snapshot {
	found, err := pdhDedicated()
	if err != nil {
		return s
	}
	s.Procs = mergeProcs(s.Procs, found)
	return s
}
