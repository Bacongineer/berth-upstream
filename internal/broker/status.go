// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Status is a copy of the broker's view: the shape /v1/status serves.

import (
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/proto"
)

// Status is a copy of the broker's view: the shape /v1/status serves.
func (b *Broker) Status() proto.Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	st := proto.Status{
		API:         proto.APIVersion,
		Version:     b.cfg.Version,
		GPUs:        []proto.GPU{},
		Leases:      []proto.Lease{},
		Waiting:     []proto.Lease{},
		Squatters:   []proto.Squatter{},
		Unprotected: []proto.Run{},
		Counters:    b.counters,
		LastEvent:   b.seq,
		Priority:    b.priorityStatus(now),
		Footprints:  b.footprintStatus(),
	}
	if b.probeErr != nil {
		st.ProbeErr = b.probeErr.Error()
	}
	devs := b.snap.Devices
	if len(devs) == 0 && b.snap.Total > 0 {
		devs = []gpu.Device{{Index: b.snap.Index, Name: b.snap.Name, Total: b.snap.Total, Used: b.snap.Used, Free: b.snap.Free,
			Util: -1, Temp: -1, Power: -1, PowerLimit: -1, Fan: -1, ClockSM: -1, ClockMem: -1, PCIeGen: -1, PCIeWidth: -1}}
	}
	for _, d := range devs {
		g := proto.GPU{Index: d.Index, Name: d.Name, Total: d.Total, Used: d.Used, Free: d.Free, Age: now.Sub(b.snap.At).Seconds(),
			Util: d.Util, Temp: d.Temp, Power: d.Power, PowerLimit: d.PowerLimit, Fan: d.Fan,
			ClockSM: d.ClockSM, ClockMem: d.ClockMem, PCIeGen: d.PCIeGen, PCIeWidth: d.PCIeWidth}
		if d.Index == b.snap.Index {
			g.Scheduled, g.Baseline = true, b.snap.Baseline()
		}
		st.GPUs = append(st.GPUs, g)
	}
	view := func(l *lease, since time.Time) proto.Lease {
		pids := make([]int, 0, len(l.gpu))
		for id := range l.gpu {
			pids = append(pids, id.pid)
		}
		sort.Ints(pids)
		return proto.Lease{
			ID: l.id, Engine: l.engine, Group: l.group, Priority: l.prio.String(), State: l.state.String(),
			Needs: l.needs, PID: l.pid, PIDStart: l.pidStart,
			Container: l.container, ContainerName: l.containerName,
			GPUPIDs: pids, Used: l.used(),
			Contested: l.contested, Age: now.Sub(since).Seconds(), Pinned: l.pinned,
		}
	}
	for _, l := range b.holders() {
		st.Leases = append(st.Leases, view(l, l.granted))
	}
	for _, l := range b.queue {
		st.Waiting = append(st.Waiting, view(l, l.arrived))
	}
	for _, s := range b.squatters {
		st.Squatters = append(st.Squatters, proto.Squatter{
			PID: s.proc.PID, PIDStart: s.id.start, Name: s.proc.Name, Used: s.proc.Used,
			Run: s.run, Container: short(s.container), ContainerName: s.containerName,
			Age: now.Sub(s.seen).Seconds(),
		})
	}
	sort.Slice(st.Squatters, func(i, j int) bool { return st.Squatters[i].PID < st.Squatters[j].PID })
	for _, c := range b.claims {
		c.PIDs = append([]int(nil), c.PIDs...)
		for _, cp := range b.claimed {
			if cp.claim == c.Name {
				c.Used += cp.proc.Used
			}
		}
		st.Claims = append(st.Claims, c)
	}
	for _, r := range b.runs {
		st.Unprotected = append(st.Unprotected, proto.Run{Engine: r.Engine, PID: r.PID, Reason: r.Reason, Age: now.Sub(r.Started).Seconds()})
	}
	return st
}
