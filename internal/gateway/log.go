// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// An engine run writes its own output to a log file, never to a pipe the
// gateway holds: a gateway that exits must not break the pipes (SIGPIPE
// would take the run and the engine with it). This file owns that file
// and the follower that copies it to the gateway journal and the tail,
// which is why both the spawner and the adopter come here.
package gateway

import (
	"io"
	"os"
	"path/filepath"
	"time"
)

// logEvery is how often a follower reads new bytes out of an engine's log.
const logEvery = 250 * time.Millisecond

// defaultLogMax is how large an engine log may grow before the follower
// starts it over. The journal keeps the copy; the file only has to hold
// the recent lines a failed start quotes.
const defaultLogMax = 64 << 20

func (g *Gateway) logMax() int64 {
	if g.LogMax > 0 {
		return g.LogMax
	}
	return defaultLogMax
}

// finalLogDrain caps one last read of a followed log: after the run ends
// the tail must have its last lines, but a grandchild still writing (a
// keep-up server) must not hold the caller of the follower.
const finalLogDrain = time.Second

// engineLogs is the directory the engines' own logs live in, made on
// first use: beside the state file, which is the one place that survives
// a gateway restart. A gateway without a state file still keeps its
// engines off pipes, with a temp directory of its own.
func (g *Gateway) engineLogs() (string, error) {
	g.logOnce.Do(func() {
		if g.StateFile != "" {
			g.logDir = filepath.Join(filepath.Dir(g.StateFile), "engines")
			g.logErr = os.MkdirAll(g.logDir, 0o700)
			return
		}
		g.logDir, g.logErr = os.MkdirTemp("", "berth-engines-")
	})
	return g.logDir, g.logErr
}

func (g *Gateway) engineLogPath(name string) (string, error) {
	dir, err := g.engineLogs()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".log"), nil
}

// followEngine feeds an engine run's log onward from offset from, until
// stop closes: the gateway's own stderr while it lives (so the journal
// shows engine logs), the tail a failed start quotes, and the lease
// watcher the record fills from. It closes done after one final read, so
// the caller knows the tail has the run's last lines.
func (g *Gateway) followEngine(name, path string, from int64, stop <-chan struct{}, done chan struct{}, s *started) {
	defer close(done)
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			g.log().Warn("gateway cannot read an engine log", "engine", name, "path", path, "err", err)
		}
		return
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		g.log().Warn("gateway cannot seek an engine log", "engine", name, "path", path, "err", err)
		return
	}
	w := &watchWriter{w: io.MultiWriter(os.Stderr, s.stderr, s.output),
		see: func(line string) {
			if lease, ok := grantedLease(line); ok {
				g.setLease(name, lease)
				g.stampGrant(s)
			}
		}}
	buf := make([]byte, 16<<10)
	drain := func(final bool) {
		deadline := time.Time{}
		if final {
			deadline = time.Now().Add(finalLogDrain)
		}
		for {
			n, rerr := f.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if rerr != nil || (final && !deadline.IsZero() && !time.Now().Before(deadline)) {
				return
			}
		}
	}
	// rotate starts the file over once it passes logMax. The run opened it
	// O_APPEND, so its next write lands at the new end; the few bytes
	// written between the last read and the truncate reach neither the
	// file nor the journal, a cost a log that would fill the disk is worth.
	rotate := func() {
		off, err := f.Seek(0, io.SeekCurrent)
		if err != nil || off < g.logMax() {
			return
		}
		if err := os.Truncate(path, 0); err == nil {
			_, _ = f.Seek(0, io.SeekStart)
		}
	}
	drain(false)
	for {
		select {
		case <-stop:
			drain(true)
			return
		case <-time.After(logEvery):
		}
		drain(false)
		rotate()
	}
}

// startFollow follows an adopted engine's log from its current end,
// until its run process is caught gone by watchAdopted.
func (g *Gateway) startFollow(name, path string, stop <-chan struct{}, s *started) error {
	st, err := os.Stat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		return nil // no log yet: an older gateway's engine, or a hand start
	}
	go g.followEngine(name, path, st.Size(), stop, make(chan struct{}), s)
	return nil
}
