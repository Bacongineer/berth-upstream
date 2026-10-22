// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
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
//
// Index picks the card on the first reading only. From then on the card is
// its PCI bus id: a card passed through to a VM leaves nvidia-smi and the
// cards after it are renumbered, and the scheduled card must not change
// with them.
type Stream struct {
	Index     int
	Bin       string
	Log       *slog.Logger
	Interval  time.Duration // stream period; default 1 s
	AppsEvery time.Duration // longest gap between process lists; default 10 s

	mu       sync.Mutex
	devices  map[string]seen // by bus id; a card that stops reporting drops out
	busID    string          // the scheduled card, pinned by the first reading that finds Index
	lastLine time.Time
	procs    []Proc
	procsAt  time.Time
	procsFor uint64 // the scheduled card's used bytes when procs was read

	// paused keeps nvidia-smi off the card (a VFIO switch): stop ends the
	// running loop, and resume wakes Run when the pause ends.
	paused bool
	stop   context.CancelFunc
	resume chan struct{}
}

// Pause stops the nvidia-smi loop at once and keeps it stopped until
// Pause(false), so nothing of berthd's holds the card open while the
// operator switches its driver. Calling it again with the same value does
// nothing.
func (s *Stream) Pause(p bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == s.paused {
		return
	}
	s.paused = p
	if p && s.stop != nil {
		s.stop()
	}
	if !p && s.resume != nil {
		close(s.resume)
		s.resume = nil
	}
}

// seen is one card's last line from the stream and when it came.
type seen struct {
	Device
	at time.Time
}

// oneShot is the one-shot probe for the card at busID ("" = by Index).
func (s *Stream) oneShot(busID string) NvidiaSMI {
	return NvidiaSMI{Index: s.Index, BusID: busID, Bin: s.Bin, Log: s.Log}
}

// pin records the scheduled card's bus id once a reading finds it. Called
// with s.mu held.
func (s *Stream) pin(busID string) {
	if s.busID == "" {
		s.busID = busID
	}
}

func (s *Stream) interval() time.Duration {
	if s.Interval <= 0 {
		return time.Second
	}
	return s.Interval
}

// Run feeds the stream until ctx ends, restarting nvidia-smi if it exits,
// and starts none while paused.
func (s *Stream) Run(ctx context.Context) {
	for ctx.Err() == nil {
		s.mu.Lock()
		if s.paused {
			wake := make(chan struct{})
			s.resume = wake
			s.mu.Unlock()
			select {
			case <-ctx.Done():
			case <-wake:
			}
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		s.stop = cancel
		s.mu.Unlock()
		s.follow(fctx)
		cancel()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Stream) follow(ctx context.Context) {
	cmd := exec.CommandContext(ctx, NvidiaSMI{Bin: s.Bin}.bin(),
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
			s.devices = map[string]seen{}
		}
		key := devs[0].BusID
		if key == "" {
			key = "index " + strconv.Itoa(devs[0].Index)
		}
		s.lastLine = time.Now()
		s.devices[key] = seen{devs[0], s.lastLine}
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
		busID := s.busID
		s.mu.Unlock()
		snap, err := s.oneShot(busID).Read(ctx)
		if err == nil {
			s.mu.Lock()
			s.pin(BusID(snap))
			s.mu.Unlock()
		}
		return snap, err
	}
	snap := Snapshot{At: s.lastLine}
	for key, c := range s.devices {
		if s.lastLine.Sub(c.at) >= 3*s.interval() {
			delete(s.devices, key) // passed through, or renumbered under another key
			continue
		}
		snap.Devices = append(snap.Devices, c.Device)
	}
	sort.Slice(snap.Devices, func(i, j int) bool { return snap.Devices[i].Index < snap.Devices[j].Index })
	d, err := pick(snap.Devices, s.Index, s.busID)
	if err != nil {
		s.mu.Unlock()
		return snap, err
	}
	s.pin(d.BusID)
	selector := s.busID
	snap.Index, snap.Name, snap.Total, snap.Used, snap.Free = d.Index, d.Name, d.Total, d.Used, d.Free
	stale := s.procsAt.IsZero() || time.Since(s.procsAt) >= appsEvery || s.procsFor != d.Used
	procs := s.procs
	s.mu.Unlock()

	if stale {
		if selector == "" {
			selector = strconv.Itoa(d.Index)
		}
		apps, err := s.oneShot("").run(ctx, "-i", selector, "--query-compute-apps=pid,process_name,used_memory", "--format=csv,noheader,nounits")
		if err != nil {
			return snap, err
		}
		parsed, err := Parse(d.Index, fmt.Sprintf("%d, %s, %d, %d, %d", d.Index, d.Name, d.Total/MiB, d.Used/MiB, d.Free/MiB), apps, snap.At)
		if err != nil {
			return snap, err
		}
		procs = parsed.Procs
		s.mu.Lock()
		s.procs, s.procsAt, s.procsFor = procs, time.Now(), d.Used
		s.mu.Unlock()
	}
	snap.Procs = procs
	return withPDH(snap, logger(s.Log)), nil
}
