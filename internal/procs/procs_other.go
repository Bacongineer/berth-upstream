//go:build !linux

package procs

// System has no parent lookup off Linux yet (Windows is v1), so only a
// wrapper's direct child PID is attributed to its lease.
type System struct{}

func (System) Parent(int) int       { return 0 }
func (System) StartTime(int) uint64 { return 0 }
