//go:build linux

package claims

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Scan reads every process from /proc. Processes that exit mid-read are
// skipped; a process this user cannot read still shows its name and parent.
func Scan() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(ents))
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		p, ok := readProc(pid)
		if ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func readProc(pid int) (Proc, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	stat, err := os.ReadFile(filepath.Join(dir, "stat"))
	if err != nil {
		return Proc{}, false
	}
	// pid (comm) state ppid ... ; comm may hold spaces and parens.
	open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return Proc{}, false
	}
	p := Proc{PID: pid, Name: string(stat[open+1 : end])}
	f := strings.Fields(string(stat[end+1:]))
	if len(f) > 19 {
		p.Parent, _ = strconv.Atoi(f[1])
		p.Start, _ = strconv.ParseUint(f[19], 10, 64)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(raw) > 0 {
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		p.Argv0 = filepath.Base(strings.ReplaceAll(args[0], `\`, "/")) // wine argv0 can be a Windows path
		p.Cmdline = strings.Join(args, " ")
	}
	return p, true
}
