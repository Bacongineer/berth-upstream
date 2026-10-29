// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package procs

import (
	"bytes"
	"strconv"
	"syscall"
)

// ReadFile reads a /proc file into buf's storage, growing it as needed:
// one open, a read until end of file, and one close, where os.ReadFile also
// stats the file and registers it with the poller. berthd reads every
// process's stat every tick, so the scans pass one buffer through.
func ReadFile(path string, buf []byte) ([]byte, error) {
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

// ReadStat reads pid's /proc/<pid>/stat into buf: its name, parent and
// start time. ok is false for a process that exited mid-read.
func ReadStat(pid int, buf []byte) (p Proc, out []byte, ok bool) {
	b, err := ReadFile("/proc/"+strconv.Itoa(pid)+"/stat", buf)
	if err != nil {
		return Proc{}, b, false
	}
	// pid (comm) state ppid ... ; comm may hold spaces and parens.
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return Proc{}, b, false
	}
	p = Proc{PID: pid, Name: string(b[open+1 : end])}
	// Fields after the comm, from state (0): ppid is 1, starttime is 19.
	f := b[end+1:]
	for i := 0; i <= 19; i++ {
		f = bytes.TrimLeft(f, " ")
		j := bytes.IndexByte(f, ' ')
		if j < 0 {
			j = len(f)
		}
		switch i {
		case 1:
			p.Parent, _ = strconv.Atoi(string(f[:j]))
		case 19:
			p.Start, _ = strconv.ParseUint(string(f[:j]), 10, 64)
			return p, b, j > 0
		}
		f = f[j:]
	}
	return p, b, false
}
