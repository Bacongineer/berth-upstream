package procs

import (
	"os"
	"strconv"
	"strings"
)

// System reads parents from /proc.
type System struct{}

func (System) Parent(pid int) int {
	f := stat(pid)
	if len(f) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(f[1])
	return ppid
}

// StartTime is field 22 of /proc/<pid>/stat: clock ticks since boot.
func (System) StartTime(pid int) uint64 {
	f := stat(pid)
	if len(f) < 20 {
		return 0
	}
	t, _ := strconv.ParseUint(f[19], 10, 64)
	return t
}

// stat returns the fields of /proc/<pid>/stat after the command name, so
// index 0 is field 3 (state). The name is "(comm)" and may contain spaces
// or parens, so the split starts after the last ')'.
func stat(pid int) []string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nil
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return nil
	}
	return strings.Fields(s[i+1:])
}
