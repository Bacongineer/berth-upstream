// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package gpu

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unsafe"

	"github.com/T-Crypt/berth/internal/procs"
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

// pdhDedicated samples every process's dedicated GPU memory: one raw entry
// per live instance, every adapter mixed together. The query stays open
// between reads and samples fresh at each collect; when a read fails the
// query is closed so the next read opens it again.
func pdhDedicated() ([]dedicatedUsage, error) {
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
	return found, nil
}

// The D3DKMT API is in gdi32, not x/sys, so it is loaded by name. The
// structures mirror d3dkmthk.h and are laid out for amd64, the only
// Windows target CI builds.

const kmtqaitypeAdapterAddress = 6 // KMTQAITYPE_ADAPTERADDRESS

var (
	gdi = windows.NewLazySystemDLL("gdi32.dll")

	d3dkmtEnumAdapters2Fn    = gdi.NewProc("D3DKMTEnumAdapters2")
	d3dkmtQueryAdapterInfoFn = gdi.NewProc("D3DKMTQueryAdapterInfo")
	d3dkmtCloseAdapterFn     = gdi.NewProc("D3DKMTCloseAdapter")
)

// d3dkmtAdapterInfo mirrors D3DKMT_ADAPTERINFO.
type d3dkmtAdapterInfo struct {
	hAdapter uint32       // D3DKMT_HANDLE, a UINT
	luid     windows.LUID // AdapterLuid
	sources  uint32       // NumOfSources
	precise  int32        // bPrecisePresentRegionsPreferred
}

// d3dkmtEnumAdapters2 mirrors D3DKMT_ENUMADAPTERS2.
type d3dkmtEnumAdapters2 struct {
	count    uint32
	adapters unsafe.Pointer
}

// d3dkmtQueryAdapterInfo mirrors D3DKMT_QUERYADAPTERINFO.
type d3dkmtQueryAdapterInfo struct {
	hAdapter uint32 // D3DKMT_HANDLE
	kind     int32
	data     unsafe.Pointer
	dataSize uint32
}

// d3dkmtAdapterAddress mirrors D3DKMT_ADAPTERADDRESS.
type d3dkmtAdapterAddress struct {
	bus, device, function uint32
}

// adapter is one D3DKMT adapter: its LUID and its PCI location.
type adapter struct {
	luid luid
	addr d3dkmtAdapterAddress
}

// closeD3DKMTAdapter releases one adapter handle opened by enumeration.
func closeD3DKMTAdapter(h uint32) {
	var c struct {
		hAdapter uint32 // D3DKMT_HANDLE
	}
	c.hAdapter = h
	_, _, _ = d3dkmtCloseAdapterFn.Call(uintptr(unsafe.Pointer(&c)))
}

// enumerateAdapters lists every adapter the display driver exposes, with its
// LUID and PCI location, through D3DKMT.
func enumerateAdapters() ([]adapter, error) {
	for _, p := range []*windows.LazyProc{d3dkmtEnumAdapters2Fn, d3dkmtQueryAdapterInfoFn, d3dkmtCloseAdapterFn} {
		if err := p.Find(); err != nil {
			return nil, err
		}
	}
	var probe d3dkmtEnumAdapters2 // nil pAdapters: count only
	if r, _, _ := d3dkmtEnumAdapters2Fn.Call(uintptr(unsafe.Pointer(&probe))); r != 0 {
		return nil, fmt.Errorf("d3dkmt enumerate adapters: status 0x%08x", r)
	}
	if probe.count == 0 {
		return nil, nil
	}
	buf := make([]d3dkmtAdapterInfo, probe.count)
	e := d3dkmtEnumAdapters2{count: uint32(len(buf)), adapters: unsafe.Pointer(&buf[0])}
	if r, _, _ := d3dkmtEnumAdapters2Fn.Call(uintptr(unsafe.Pointer(&e))); r != 0 {
		return nil, fmt.Errorf("d3dkmt enumerate adapters: status 0x%08x", r)
	}
	out := make([]adapter, 0, e.count)
	for i := uint32(0); i < e.count; i++ {
		a := buf[i]
		var addr d3dkmtAdapterAddress
		q := d3dkmtQueryAdapterInfo{
			hAdapter: a.hAdapter,
			kind:     kmtqaitypeAdapterAddress,
			data:     unsafe.Pointer(&addr),
			dataSize: uint32(unsafe.Sizeof(addr)),
		}
		if r, _, _ := d3dkmtQueryAdapterInfoFn.Call(uintptr(unsafe.Pointer(&q))); r != 0 {
			closeD3DKMTAdapter(a.hAdapter)
			continue
		}
		closeD3DKMTAdapter(a.hAdapter)
		out = append(out, adapter{luid: luid{hi: uint32(a.luid.HighPart), lo: a.luid.LowPart}, addr: addr})
	}
	return out, nil
}

var (
	adapterMu   sync.Mutex
	adapterList []adapter
	adapterErr  error
	adapterDone bool
)

// enumerateAdaptersCached lists the adapters once per run; their LUIDs and
// PCI locations are stable for the life of the daemon.
func enumerateAdaptersCached() ([]adapter, error) {
	adapterMu.Lock()
	defer adapterMu.Unlock()
	if adapterDone {
		return adapterList, adapterErr
	}
	adapterList, adapterErr = enumerateAdapters()
	adapterDone = true
	return adapterList, adapterErr
}

// targetLUID pins the governed card to the one adapter LUID whose PCI
// location is the card's pci.bus_id, so per-process figures from the other
// adapters on the box are dropped. It reports an error when no single adapter
// matches, which the caller takes as "keep every adapter".
func targetLUID(s Snapshot) (map[luid]bool, error) {
	id := BusID(s)
	bus, dev, fn, ok := parseBusID(id)
	if !ok {
		return nil, fmt.Errorf("the governed card's pci.bus_id %q does not parse", id)
	}
	adapters, err := enumerateAdaptersCached()
	if err != nil {
		return nil, err
	}
	var hit luid
	matches := 0
	for _, a := range adapters {
		if a.addr.bus == bus && a.addr.device == dev && a.addr.function == fn {
			matches++
			hit = a.luid
		}
	}
	if matches != 1 {
		return nil, fmt.Errorf("pci.bus_id %s matches %d adapters, not one", id, matches)
	}
	return map[luid]bool{hit: true}, nil
}

var fallbackLogOnce sync.Once

// logFallback records, once per run, why the governed card's adapter could
// not be pinned and Berth is keeping every adapter's LUID.
func logFallback(log *slog.Logger, reason error) {
	log = logger(log)
	fallbackLogOnce.Do(func() {
		log.Warn("pdh: keeping every adapter's LUID", "reason", reason.Error())
	})
}

var (
	capLogMu   sync.Mutex
	capLogLast = map[int]time.Time{}
)

// logCapped records a PID whose reading was capped to the card's total, at
// most once per PID per minute, so a bogus counter is visible without
// flooding the journal.
func logCapped(log *slog.Logger, reads []cappedRead) {
	if len(reads) == 0 {
		return
	}
	log = logger(log)
	now := time.Now()
	capLogMu.Lock()
	defer capLogMu.Unlock()
	for _, r := range reads {
		if now.Sub(capLogLast[r.pid]) < time.Minute {
			continue
		}
		capLogLast[r.pid] = now
		log.Warn("pdh reading capped to the card's total", "pid", r.pid, "raw", r.raw, "total", r.total)
	}
}

// processNames reads the process table's exe name for every PID, from one
// Toolhelp32 snapshot. The walk needs no OpenProcess, so protected
// processes such as dwm.exe and csrss.exe are named too. A nil map means
// the table could not be read and no name is available.
func processNames() map[int]string {
	names := map[int]string{}
	if err := procs.Walk(func(e *windows.ProcessEntry32) {
		names[int(e.ProcessID)] = windows.UTF16ToString(e.ExeFile[:])
	}); err != nil {
		return nil
	}
	return names
}

// withPDH merges PDH's per-process reading into a parsed snapshot. The
// driver's totals stand; PDH is Windows' only view of a process's own
// VRAM under WDDM. Only the governed card's adapter is counted, and a PID's
// figure is capped at the card's total, so a driver's committed reservation
// cannot read as more VRAM than the card holds. A PDH failure, which happens
// on a machine with no GPU and can happen on one with a GPU, keeps the
// snapshot as nvidia-smi gave it and the next read retries.
func withPDH(s Snapshot, log *slog.Logger) Snapshot {
	found, err := pdhDedicated()
	if err != nil {
		return s
	}
	want, terr := targetLUID(s)
	if terr != nil {
		logFallback(log, terr)
		want = nil // keep every adapter
	}
	byPid := dedicatedByPid(filterByLuid(found, want))
	capped, reads := capToTotal(byPid, s.Total)
	logCapped(log, reads)
	var names map[int]string
	if len(capped) > 0 {
		names = processNames()
	}
	s.Procs = mergeProcs(s.Procs, capped, names)
	return s
}
