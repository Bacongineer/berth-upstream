// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Stream keeps one nvidia-smi running in loop mode for every card's memory,
// instead of starting two processes every poll, and lists the scheduled
// card's processes only when its used memory moves or AppsEvery passes.
// Memory can only be held by a process, so a process that appears or goes
// away moves the used figure, and the periodic list catches anything else.
// If the stream stalls, Read falls back to a one-shot query, so a dead
// stream costs freshness, never correctness.
type Stream struct {
	Index     int
	Bin       string
	Interval  time.Duration // stream period; default 1 s
	AppsEvery time.Duration // longest gap between process lists; default 10 s

	mu       sync.Mutex
	devices  map[int]Device
	lastLine time.Time
	procs    []Proc
	procsAt  time.Time
	procsFor uint64 // the scheduled card's used bytes when procs was read
}

func (s *Stream) oneShot() NvidiaSMI { return NvidiaSMI{Index: s.Index, Bin: s.Bin} }

func (s *Stream) interval() time.Duration {
	if s.Interval <= 0 {
		return time.Second
	}
	return s.Interval
}

// Run feeds the stream until ctx ends, restarting nvidia-smi if it exits.
func (s *Stream) Run(ctx context.Context) {
	for ctx.Err() == nil {
		s.follow(ctx)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Stream) follow(ctx context.Context) {
	cmd := exec.CommandContext(ctx, s.oneShot().bin(),
		"--query-gpu="+Query, "--format=csv,noheader,nounits",
		"-lms", strconv.FormatInt(s.interval().Milliseconds(), 10))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err := cmd.Start(); err != nil {
		return
	}
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		devs, err := ParseDevices(sc.Text())
		if err != nil || len(devs) != 1 {
			continue
		}
		s.mu.Lock()
		if s.devices == nil {
			s.devices = map[int]Device{}
		}
		s.devices[devs[0].Index] = devs[0]
		s.lastLine = time.Now()
		s.mu.Unlock()
	}
	_ = cmd.Wait()
}

func (s *Stream) Read(ctx context.Context) (Snapshot, error) {
	appsEvery := s.AppsEvery
	if appsEvery <= 0 {
		appsEvery = 10 * time.Second
	}
	s.mu.Lock()
	fresh := !s.lastLine.IsZero() && time.Since(s.lastLine) < 3*s.interval()
	if !fresh {
		s.mu.Unlock()
		return s.oneShot().Read(ctx)
	}
	snap := Snapshot{At: s.lastLine}
	for _, d := range s.devices {
		snap.Devices = append(snap.Devices, d)
	}
	sort.Slice(snap.Devices, func(i, j int) bool { return snap.Devices[i].Index < snap.Devices[j].Index })
	d, ok := s.devices[s.Index]
	if !ok {
		s.mu.Unlock()
		return snap, fmt.Errorf("no GPU with index %d (found %d)", s.Index, len(s.devices))
	}
	snap.Index, snap.Name, snap.Total, snap.Used, snap.Free = d.Index, d.Name, d.Total, d.Used, d.Free
	stale := s.procsAt.IsZero() || time.Since(s.procsAt) >= appsEvery || s.procsFor != d.Used
	procs := s.procs
	s.mu.Unlock()

	if stale {
		apps, err := s.oneShot().run(ctx, "-i", strconv.Itoa(s.Index), "--query-compute-apps=pid,process_name,used_memory", "--format=csv,noheader,nounits")
		if err != nil {
			return snap, err
		}
		parsed, err := Parse(s.Index, fmt.Sprintf("%d, %s, %d, %d, %d", d.Index, d.Name, d.Total/MiB, d.Used/MiB, d.Free/MiB), apps, snap.At)
		if err != nil {
			return snap, err
		}
		procs = parsed.Procs
		s.mu.Lock()
		s.procs, s.procsAt, s.procsFor = procs, time.Now(), d.Used
		s.mu.Unlock()
	}
	snap.Procs = procs
	return withPDH(snap), nil
}
