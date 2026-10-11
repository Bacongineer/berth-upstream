// Package procs answers one question: is this process a descendant of that
// one? Berth uses it to attribute a GPU process to the lease whose wrapper
// started it, even when the engine forks (Strata's server.py spawns the
// engine binary that actually holds the memory).
package procs

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
