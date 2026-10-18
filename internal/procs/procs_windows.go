//go:build windows

package procs

import (
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// System reads parents from a Toolhelp32 snapshot and start times from
// GetProcessTimes. Windows reuses PIDs and keeps a child's parent PID after
// the parent exits, so a parent only counts if it started before the child.
type System struct{}

// parentsTTL bounds how stale a parent lookup may be. The broker walks
// ancestors for every GPU process each tick; one snapshot serves them all.
const parentsTTL = time.Second

var parents struct {
	mu  sync.Mutex
	at  time.Time
	ppx map[int]int
}

func (s System) Parent(pid int) int {
	parents.mu.Lock()
	if time.Since(parents.at) > parentsTTL || parents.ppx == nil {
		if m, err := snapshotParents(); err == nil {
			parents.ppx, parents.at = m, time.Now()
		}
	}
	ppid := parents.ppx[pid]
	parents.mu.Unlock()
	if ppid <= 0 {
		return 0
	}
	if cs, ps := s.StartTime(pid), s.StartTime(ppid); cs == 0 || ps == 0 || ps > cs {
		return 0 // the parent is gone and its PID belongs to someone newer
	}
	return ppid
}

// StartTime is the process creation time in 100 ns units since 1601.
func (System) StartTime(pid int) uint64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)
}

func snapshotParents() (map[int]int, error) {
	m := map[int]int{}
	err := Walk(func(e *windows.ProcessEntry32) {
		m[int(e.ProcessID)] = int(e.ParentProcessID)
	})
	return m, err
}

// Walk calls fn for every process in a Toolhelp32 snapshot.
func Walk(fn func(*windows.ProcessEntry32)) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		fn(&e)
	}
	if err == windows.ERROR_NO_MORE_FILES {
		return nil
	}
	return err
}
