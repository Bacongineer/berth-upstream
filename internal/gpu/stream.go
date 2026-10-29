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
	"strings"
	"sync"
	"time"
)

// Stream is the machine's one NVIDIA reader. It keeps one nvidia-smi running
// in loop mode for every card's memory, instead of starting two processes
// every poll for each card, and lists the processes of every card in one
// query, only when a governed card's used memory moves or AppsEvery passes.
// Memory can only be held by a process, so a process that appears or goes
// away moves the used figure, and the periodic list catches anything else.
// If the stream stalls, a read falls back to a one-shot query, so a dead
// stream costs freshness, never correctness.
//
// Each governed card reads through its StreamCard from Card. A card's Index
// picks it on the first reading only. From then on the card is its PCI bus
// id: a card passed through to a VM leaves nvidia-smi and the cards after it
// are renumbered, and a governed card must not change with them.
type Stream struct {
	Bin       string
	Log       *slog.Logger
	Interval  time.Duration // stream period; default 1 s
	AppsEvery time.Duration // longest gap between process lists; default 10 s

	mu       sync.Mutex
	cards    []*StreamCard
	devices  map[string]seen // by bus id; a card that stops reporting drops out
	lastLine time.Time
	procs    map[string][]Proc // by sysfs address, from the last process list
	procsAt  time.Time
	procsFor map[string]uint64 // each card's used bytes when procs was read

	// lists runs one process list at a time, so cards whose reads fall due
	// together share one nvidia-smi.
	lists sync.Mutex

	// A change to which cards are read (a pause) ends the running loop
	// through stop, and wakes Run through wake when it waits with nothing
	// to read.
	stop context.CancelFunc
	wake chan struct{}
	gen  uint64 // counts selection changes
}

// StreamCard is one governed card's probe over the machine's Stream.
type StreamCard struct {
	s      *Stream
	index  int
	busID  string // the card: --gpu's address, or pinned by the first reading that finds index
	paused bool
}

// Card is the probe for the card at index, or at addr when it is set. A
// card's reads share the stream's one loop and one process list.
func (s *Stream) Card(index int, addr string) *StreamCard {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := &StreamCard{s: s, index: index, busID: addr}
	s.cards = append(s.cards, c)
	s.changed()
	return c
}

// Pause stops nvidia-smi on this card at once and keeps it off until
// Pause(false), so nothing of berthd's holds the card open while the
// operator switches its driver. The machine's other cards go on being read
// by address. Calling it again with the same value does nothing.
func (c *StreamCard) Pause(p bool) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if p == c.paused {
		return
	}
	c.paused = p
	if !p && c.busID != "" {
		// The card's processes were last listed before the switch.
		delete(s.procsFor, SysfsAddr(c.busID))
	}
	s.changed()
}

// changed restarts the loop with the cards to read now. Readings taken
// under the old selection no longer count as fresh. Called with s.mu held.
func (s *Stream) changed() {
	s.gen++
	s.lastLine = time.Time{}
	if s.stop != nil {
		s.stop()
		s.stop = nil
	}
	if s.wake != nil {
		close(s.wake)
		s.wake = nil
	}
}

// selection is what nvidia-smi is asked to read: every card it sees ("")
// while no card is paused, or the unpaused governed cards by address while
// one is. ok is false when there is nothing to read: no card is governed,
// or every one is paused. Called with s.mu held.
func (s *Stream) selection() (sel string, ok bool) {
	var paused bool
	var ids []string
	for _, c := range s.cards {
		switch {
		case c.paused:
			paused = true
		case c.busID != "":
			ids = append(ids, c.busID)
		default:
			ids = append(ids, strconv.Itoa(c.index))
		}
	}
	if !paused {
		return "", len(s.cards) > 0
	}
	return strings.Join(ids, ","), len(ids) > 0
}

// args is nvidia-smi's arguments for sel, as selection gives it.
func args(sel string, query ...string) []string {
	if sel == "" {
		return query
	}
	return append([]string{"-i", sel}, query...)
}

// seen is one card's last line from the stream and when it came.
type seen struct {
	Device
	at time.Time
}

func (s *Stream) interval() time.Duration {
	if s.Interval <= 0 {
		return time.Second
	}
	return s.Interval
}

func (s *Stream) appsEvery() time.Duration {
	if s.AppsEvery <= 0 {
		return 10 * time.Second
	}
	return s.AppsEvery
}

func (s *Stream) smi() NvidiaSMI { return NvidiaSMI{Bin: s.Bin, Log: s.Log} }

// Run feeds the stream until ctx ends, restarting nvidia-smi if it exits,
// and starts none while there is nothing to read.
func (s *Stream) Run(ctx context.Context) {
	for ctx.Err() == nil {
		s.mu.Lock()
		sel, ok := s.selection()
		if !ok {
			wake := make(chan struct{})
			s.wake = wake
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
		s.follow(fctx, sel)
		changed := fctx.Err() != nil
		cancel()
		if changed {
			continue // the selection changed: read the new one at once
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Stream) follow(ctx context.Context, sel string) {
	cmd := exec.CommandContext(ctx, s.smi().bin(), args(sel,
		"--query-gpu="+Query, "--format=csv,noheader,nounits",
		"-lms", strconv.FormatInt(s.interval().Milliseconds(), 10))...)
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
		if ctx.Err() == nil {
			if s.devices == nil {
				s.devices = map[string]seen{}
			}
			key := devs[0].BusID
			if key == "" {
				key = "index " + strconv.Itoa(devs[0].Index)
			}
			s.lastLine = time.Now()
			s.devices[key] = seen{devs[0], s.lastLine}
		}
		s.mu.Unlock()
	}
	_ = cmd.Wait()
}

// Read is the card's snapshot: its memory from the stream's last lines, or
// from a one-shot query when the stream is stale, and its processes from
// the machine's last process list.
func (c *StreamCard) Read(ctx context.Context) (Snapshot, error) {
	s := c.s
	s.mu.Lock()
	sel, _ := s.selection()
	var snap Snapshot
	if !s.lastLine.IsZero() && time.Since(s.lastLine) < 3*s.interval() {
		snap = Snapshot{At: s.lastLine}
		for key, d := range s.devices {
			if s.lastLine.Sub(d.at) >= 3*s.interval() {
				delete(s.devices, key) // passed through, or renumbered under another key
				continue
			}
			snap.Devices = append(snap.Devices, d.Device)
		}
		sort.Slice(snap.Devices, func(i, j int) bool { return snap.Devices[i].Index < snap.Devices[j].Index })
		s.mu.Unlock()
	} else {
		s.mu.Unlock()
		mem, err := s.smi().run(ctx, args(sel, "--query-gpu="+Query, "--format=csv,noheader,nounits")...)
		if err != nil {
			return Snapshot{}, err
		}
		if snap.Devices, err = ParseDevices(mem); err != nil {
			return Snapshot{}, err
		}
		snap.At = time.Now()
	}

	s.mu.Lock()
	d, err := pick(snap.Devices, c.index, c.busID)
	if err != nil {
		s.mu.Unlock()
		return snap, err
	}
	if c.busID == "" {
		c.busID = d.BusID
	}
	snap.Index, snap.Name, snap.Total, snap.Used, snap.Free = d.Index, d.Name, d.Total, d.Used, d.Free
	s.mu.Unlock()

	procs, err := s.procsOf(ctx, d)
	if err != nil {
		return snap, err
	}
	snap.Procs = procs
	return withPDH(snap, logger(s.Log)), nil
}

// procsOf is the processes on card d from the machine's process list,
// listing every card read now again when d's list is stale.
func (s *Stream) procsOf(ctx context.Context, d Device) ([]Proc, error) {
	key := SysfsAddr(d.BusID)
	stale := func() bool {
		used, ok := s.procsFor[key]
		return !ok || used != d.Used || time.Since(s.procsAt) >= s.appsEvery()
	}
	s.mu.Lock()
	if !stale() {
		defer s.mu.Unlock()
		return s.procs[key], nil
	}
	s.mu.Unlock()

	s.lists.Lock()
	defer s.lists.Unlock()
	s.mu.Lock()
	if !stale() { // another card's read listed them while this one waited
		defer s.mu.Unlock()
		return s.procs[key], nil
	}
	sel, _ := s.selection()
	gen := s.gen
	s.mu.Unlock()
	apps, err := s.smi().run(ctx, args(sel, "--query-compute-apps=gpu_bus_id,pid,process_name,used_memory", "--format=csv,noheader,nounits")...)
	if err != nil {
		return nil, err
	}
	byCard, err := ParseApps(apps)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procs, s.procsAt = byCard, time.Now()
	s.procsFor = map[string]uint64{}
	if gen != s.gen {
		// A card was paused or resumed while the list ran: it counts for
		// no card, and every card is listed again on its next read.
		return s.procs[key], nil
	}
	// The list is current only for the cards it asked about: a card left
	// out (paused when sel was taken) is listed again on its next read.
	asked := map[string]bool{}
	for _, id := range strings.Split(sel, ",") {
		asked[SysfsAddr(id)] = true
	}
	for _, seen := range s.devices {
		if k := SysfsAddr(seen.BusID); sel == "" || asked[k] {
			s.procsFor[k] = seen.Used
		}
	}
	s.procsFor[key] = d.Used
	return s.procs[key], nil
}

// ParseApps reads "gpu_bus_id, pid, process_name, used_memory" lines (memory
// in MiB) into each card's processes, keyed by the card's sysfs address.
func ParseApps(apps string) (map[string][]Proc, error) {
	out := map[string][]Proc{}
	for _, line := range strings.Split(apps, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "No running") {
			continue
		}
		bus, rest, ok := strings.Cut(line, ",")
		addr := SysfsAddr(strings.TrimSpace(bus))
		if !ok || addr == "" {
			return nil, fmt.Errorf("compute-apps line %q", line)
		}
		p, err := parseApp(rest)
		if err != nil {
			return nil, fmt.Errorf("compute-apps line %q: %w", line, err)
		}
		out[addr] = append(out[addr], p)
	}
	return out, nil
}

// parseApp reads "pid, process_name, used_memory". The name may itself
// contain commas: the PID is before the first one and the memory figure
// after the last.
func parseApp(line string) (Proc, error) {
	i, j := strings.IndexByte(line, ','), strings.LastIndexByte(line, ',')
	if i < 0 || i == j {
		return Proc{}, fmt.Errorf("want pid, name, memory")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:i]))
	if err != nil {
		return Proc{}, fmt.Errorf("pid: %w", err)
	}
	used, err := strconv.ParseUint(strings.TrimSpace(line[j+1:]), 10, 64)
	if err != nil {
		// "[N/A]" on some drivers and in some containers.
		used = 0
	}
	return Proc{PID: pid, Name: strings.TrimSpace(line[i+1 : j]), Used: used * MiB}, nil
}
