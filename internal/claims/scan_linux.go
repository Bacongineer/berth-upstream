// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package claims

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Scan reads every process from /proc. Processes that exit mid-read are
// skipped; a process this user cannot read still shows its name and parent.
// It runs every two seconds over every process on the machine, so each file
// is read with one open, read and close into one buffer.
func Scan() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(ents))
	buf := make([]byte, 0, 4096)
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		var p Proc
		var ok bool
		if p, buf, ok = readProc(pid, buf); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func readProc(pid int, buf []byte) (Proc, []byte, bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	stat, err := readFile(dir+"/stat", buf)
	if err != nil {
		return Proc{}, stat, false
	}
	// pid (comm) state ppid ... ; comm may hold spaces and parens.
	open, end := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return Proc{}, stat, false
	}
	p := Proc{PID: pid, Name: string(stat[open+1 : end])}
	// Fields after the comm, from state (0): ppid is 1, starttime is 19.
	if f := fields(stat[end+1:], 19); len(f) > 19 {
		p.Parent, _ = strconv.Atoi(string(f[1]))
		p.Start, _ = strconv.ParseUint(string(f[19]), 10, 64)
	}
	raw, err := readFile(dir+"/cmdline", stat)
	if err == nil && len(raw) > 0 {
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		p.Argv0 = filepath.Base(strings.ReplaceAll(args[0], `\`, "/")) // wine argv0 can be a Windows path
		p.Cmdline = strings.Join(args, " ")
	}
	return p, raw, true
}

// fields splits b on spaces up to and including field last, without
// copying it.
func fields(b []byte, last int) [][]byte {
	f := make([][]byte, 0, last+1)
	for len(b) > 0 && len(f) <= last {
		b = bytes.TrimLeft(b, " ")
		i := bytes.IndexByte(b, ' ')
		if i < 0 {
			i = len(b)
		}
		if i > 0 {
			f = append(f, b[:i])
		}
		b = b[i:]
	}
	return f
}

// readFile reads a /proc file into buf's storage, growing it as needed:
// one open, a read until end of file, and one close, where os.ReadFile also
// stats the file and registers it with the poller.
func readFile(path string, buf []byte) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return buf[:0], err
	}
	defer syscall.Close(fd)
	buf = buf[:0]
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := syscall.Read(fd, buf[len(buf):cap(buf)])
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return buf[:0], err
		}
		if n == 0 {
			return buf, nil
		}
		buf = buf[:len(buf)+n]
	}
}
