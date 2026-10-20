// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package gpu reads what the card reports: total, used and free memory, and
// the compute processes holding it. It shells out to nvidia-smi, which talks
// to NVML and never opens a CUDA context, so probing costs no VRAM.
package gpu

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

const MiB = 1 << 20

// Proc is one compute process holding memory on the card.
type Proc struct {
	PID  int
	Name string
	Used uint64 // bytes
}

// Device is one card as the driver reports it. Telemetry the driver does
// not report ("[N/A]") reads -1, never a made-up zero.
type Device struct {
	Index int
	Name  string
	Total uint64
	Used  uint64
	Free  uint64

	Util       float64 // %
	Temp       float64 // C
	Power      float64 // W
	PowerLimit float64 // W
	Fan        float64 // %
	ClockSM    float64 // MHz
	ClockMem   float64 // MHz
	PCIeGen    float64
	PCIeWidth  float64
}

// Query is the per-card field list, memory first.
const Query = "index,name,memory.total,memory.used,memory.free,utilization.gpu,temperature.gpu,power.draw,power.limit,fan.speed,clocks.sm,clocks.mem,pcie.link.gen.current,pcie.link.width.current"

// Snapshot is one reading: the scheduled card in full, every card's memory
// in Devices, so a multi-GPU box is inventoried with its real limits.
type Snapshot struct {
	Devices []Device
	Index   int
	Name    string
	Total   uint64
	Used    uint64
	Free    uint64
	Procs   []Proc
	At      time.Time
}

// Baseline is memory in use that no compute process accounts for: the
// desktop, the driver, graphics clients nvidia-smi does not list.
func (s Snapshot) Baseline() uint64 {
	var procs uint64
	for _, p := range s.Procs {
		procs += p.Used
	}
	if procs >= s.Used {
		return 0
	}
	return s.Used - procs
}

// graphicsProcBytes is how much dedicated VRAM a process nvidia-smi does
// not list must hold before it joins the snapshot's process list. Desktop
// compositors sit well under it; a game, a renderer or a stray compute
// process does not.
const graphicsProcBytes = 64 * MiB

// instancePID reads the PID out of a "GPU Process Memory" instance name,
// which the engine dresses as "pid_1234_luid_0x00000000_0x00003f9d_phys_0".
func instancePID(instance string) (int, bool) {
	const prefix = "pid_"
	if !strings.HasPrefix(instance, prefix) {
		return 0, false
	}
	digits := strings.TrimPrefix(instance, prefix)
	if i := strings.IndexByte(digits, '_'); i >= 0 {
		digits = digits[:i]
	}
	pid, err := strconv.Atoi(digits)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// dedicatedUsage is one PDH sample: the instance name and its Dedicated
// Usage in bytes.
type dedicatedUsage struct {
	Instance string
	Bytes    uint64
}

// dedicatedByPid sums one read's samples by the PID their instance names
// carry. One process can hold both cards of a multi-adapter board, so a
// PID's figures come together. An instance that is not a process (none is
// known) and a zero sample contribute nothing.
func dedicatedByPid(items []dedicatedUsage) map[int]uint64 {
	out := map[int]uint64{}
	for _, it := range items {
		pid, ok := instancePID(it.Instance)
		if !ok || it.Bytes == 0 {
			continue
		}
		out[pid] += it.Bytes
	}
	return out
}

// mergeProcs folds PDH's dedicated-memory figures into the compute-app
// list. A listed process whose Used is 0 (WDDM reports [N/A]) takes the
// PDH figure, and nvidia-smi's own number wins when it has one. A PID the
// list lacks, a graphics client or a game, joins when its dedicated use
// clears graphicsProcBytes; PDH does not say its name, so the Name stays
// empty. Card totals still come from the driver.
func mergeProcs(procs []Proc, dedicated map[int]uint64) []Proc {
	if len(dedicated) == 0 {
		return procs
	}
	listed := make(map[int]bool, len(procs))
	out := make([]Proc, 0, len(procs)+len(dedicated))
	for _, p := range procs {
		listed[p.PID] = true
		if p.Used == 0 {
			if d := dedicated[p.PID]; d > 0 {
				p.Used = d
			}
		}
		out = append(out, p)
	}
	var fresh []int
	for pid, d := range dedicated {
		if !listed[pid] && d > graphicsProcBytes {
			fresh = append(fresh, pid)
		}
	}
	sort.Ints(fresh)
	for _, pid := range fresh {
		out = append(out, Proc{PID: pid, Used: dedicated[pid]})
	}
	return out
}

// Probe reads the card.
type Probe interface {
	Read(ctx context.Context) (Snapshot, error)
}

// NvidiaSMI probes one GPU by index through nvidia-smi.
type NvidiaSMI struct {
	Index int
	Bin   string // default "nvidia-smi"
}

func (n NvidiaSMI) bin() string {
	if n.Bin == "" {
		return "nvidia-smi"
	}
	return n.Bin
}

func (n NvidiaSMI) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, n.bin(), args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", n.bin(), strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (n NvidiaSMI) Read(ctx context.Context) (Snapshot, error) {
	mem, err := n.run(ctx, "--query-gpu="+Query, "--format=csv,noheader,nounits")
	if err != nil {
		return Snapshot{}, err
	}
	apps, err := n.run(ctx, "-i", strconv.Itoa(n.Index), "--query-compute-apps=pid,process_name,used_memory", "--format=csv,noheader,nounits")
	if err != nil {
		return Snapshot{}, err
	}
	s, err := Parse(n.Index, mem, apps, time.Now())
	if err != nil {
		return s, err
	}
	return withPDH(s), nil
}

// ParseDevices reads "index, name, total, used, free[, telemetry...]" lines
// (memory in MiB), one per card, in the order of Query.
func ParseDevices(mem string) ([]Device, error) {
	var out []Device
	for _, line := range strings.Split(mem, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		f := splitCSV(line)
		if len(f) != 5 && len(f) != 14 {
			return nil, fmt.Errorf("gpu query: want 5 or 14 fields, got %q", line)
		}
		idx, err := strconv.Atoi(f[0])
		if err != nil {
			return nil, fmt.Errorf("memory query index %q: %w", f[0], err)
		}
		d := Device{Index: idx, Name: f[1]}
		for i, dst := range []*uint64{&d.Total, &d.Used, &d.Free} {
			n, err := strconv.ParseUint(f[2+i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("memory query %q field %d: %w", line, 2+i, err)
			}
			*dst = n * MiB
		}
		tel := []*float64{&d.Util, &d.Temp, &d.Power, &d.PowerLimit, &d.Fan, &d.ClockSM, &d.ClockMem, &d.PCIeGen, &d.PCIeWidth}
		for i, dst := range tel {
			*dst = -1
			if len(f) == 14 {
				if v, err := strconv.ParseFloat(f[5+i], 64); err == nil {
					*dst = v
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// Parse builds the snapshot for the card at index from nvidia-smi's
// csv,noheader,nounits output: mem lists every card, apps that card's processes.
func Parse(index int, mem, apps string, at time.Time) (Snapshot, error) {
	s := Snapshot{At: at}
	devs, err := ParseDevices(mem)
	if err != nil {
		return s, err
	}
	s.Devices = devs
	found := false
	for _, d := range devs {
		if d.Index == index {
			s.Index, s.Name, s.Total, s.Used, s.Free = d.Index, d.Name, d.Total, d.Used, d.Free
			found = true
		}
	}
	if !found {
		return s, fmt.Errorf("no GPU with index %d (found %d)", index, len(devs))
	}

	for _, line := range strings.Split(apps, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "No running") {
			continue
		}
		// The name may itself contain commas: the PID is before the first
		// one and the memory figure after the last.
		i, j := strings.IndexByte(line, ','), strings.LastIndexByte(line, ',')
		if i < 0 || i == j {
			return s, fmt.Errorf("compute-apps line %q", line)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[:i]))
		if err != nil {
			return s, fmt.Errorf("compute-apps pid in %q: %w", line, err)
		}
		used, err := strconv.ParseUint(strings.TrimSpace(line[j+1:]), 10, 64)
		if err != nil {
			// "[N/A]" on some drivers and in some containers.
			used = 0
		}
		s.Procs = append(s.Procs, Proc{PID: pid, Name: strings.TrimSpace(line[i+1 : j]), Used: used * MiB})
	}
	return s, nil
}

func splitCSV(line string) []string {
	parts := strings.Split(line, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
