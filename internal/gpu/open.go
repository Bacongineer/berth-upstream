// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrUnsafeRead means a probe cannot establish a trustworthy observation.
// Unlike the historical NVIDIA fallback, it must block new admissions.
var ErrUnsafeRead = errors.New("GPU observation unavailable")

// Options selects a backend and its card. AMD indices are DRM card numbers,
// NVIDIA indices are nvidia-smi indices; neither is a HIP/CUDA ordinal.
type Options struct {
	Backend string
	Index   int
	// Addr pins an NVIDIA card by its PCI address in sysfs form
	// ("0000:02:00.0") from the first read, so berthd can start while the
	// card is in a VM and Index is not read.
	Addr     string
	Interval time.Duration
	// Stream is the machine's NVIDIA reader the card reads through; nil =
	// a one-shot nvidia-smi query per read.
	Stream *Stream
	Log    *slog.Logger
	// Roots are injectable for fixtures; empty means the host's sysfs/procfs.
	SysRoot  string
	ProcRoot string
}

// ValidateSelection checks flags without reading hardware.
func ValidateSelection(backend string, index int) error {
	if index < 0 {
		return fmt.Errorf("GPU index must be non-negative")
	}
	switch backend {
	case "", "auto", "nvidia", "amd":
		return nil
	default:
		return fmt.Errorf("GPU backend must be auto, nvidia or amd, got %q", backend)
	}
}

// ParseCard reads a --gpu value: a PCI address ("0000:02:00.0", or
// nvidia-smi's "00000000:02:00.0"), returned in sysfs form, or an index.
func ParseCard(s string) (index int, addr string, err error) {
	if strings.Contains(s, ":") {
		if addr = SysfsAddr(s); addr == "" {
			return 0, "", fmt.Errorf("GPU address must be a PCI address with its domain, like 0000:02:00.0, got %q", s)
		}
		return 0, addr, nil
	}
	index, err = strconv.Atoi(s)
	if err != nil {
		return 0, "", fmt.Errorf("GPU must be an index or a PCI address like 0000:02:00.0, got %q", s)
	}
	return index, "", nil
}

// Open reuses Probe for both vendors. Auto keeps NVIDIA whenever nvidia-smi
// is installed, as before AMD support, so a unit without --gpu-backend on an
// NVIDIA host with a Phoenix iGPU still starts; AMD is chosen only without it.
func Open(o Options) (Probe, error) {
	if err := ValidateSelection(o.Backend, o.Index); err != nil {
		return nil, err
	}
	backend, err := pickBackend(o)
	if err != nil {
		return nil, err
	}
	if backend == "amd" {
		if o.Addr != "" {
			return nil, fmt.Errorf("an AMD card is selected by its DRM cardN number, not a PCI address")
		}
		return openAMD(o)
	}
	if o.Stream != nil {
		return o.Stream.Card(o.Index, o.Addr), nil
	}
	return NvidiaSMI{Index: o.Index, BusID: o.Addr, Log: o.Log}, nil
}

// pickBackend resolves auto: NVIDIA whenever nvidia-smi is installed, AMD
// only without it.
func pickBackend(o Options) (string, error) {
	if o.Backend != "" && o.Backend != "auto" {
		return o.Backend, nil
	}
	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		return "nvidia", nil
	}
	amd, err := hasAMD(o)
	if err != nil {
		return "", err
	}
	if !amd {
		return "", fmt.Errorf("no supported GPU backend: need nvidia-smi or Linux amdgpu with an RDNA3/RDNA4 discrete GPU or Phoenix APU")
	}
	return "amd", nil
}

// List is every card the backend can govern, in index order, from one
// reading: the cards berthd --gpu all opens. An NVIDIA card in a VM is not
// listed; berthd governs it only when --gpu names its address.
func List(ctx context.Context, o Options) ([]Device, error) {
	if err := ValidateSelection(o.Backend, 0); err != nil {
		return nil, err
	}
	backend, err := pickBackend(o)
	if err != nil {
		return nil, err
	}
	if backend == "amd" {
		return listAMD(o)
	}
	mem, err := NvidiaSMI{Log: o.Log}.run(ctx, "--query-gpu="+Query, "--format=csv,noheader,nounits")
	if err != nil {
		return nil, err
	}
	return ParseDevices(mem)
}

// CheckCard accepts the card a run or an engine entry names: its name on
// the node (gpu plus its index) or its PCI address, or a comma list of them
// for one lease on several cards.
func CheckCard(s string) error {
	seen := map[string]bool{}
	for _, c := range strings.Split(s, ",") {
		if !cardName(c) {
			return fmt.Errorf("gpu %q must be a card name like gpu1 or a PCI address like 0000:41:00.0, or a comma list of them", s)
		}
		if seen[c] {
			return fmt.Errorf("gpu %q names %s twice", s, c)
		}
		seen[c] = true
	}
	return nil
}

// cardName is whether s is one card's name on a node or its PCI address.
func cardName(s string) bool {
	if strings.Contains(s, ":") {
		return SysfsAddr(s) != ""
	}
	n, ok := strings.CutPrefix(s, "gpu")
	_, err := strconv.Atoi(n)
	return ok && err == nil && !strings.HasPrefix(n, "-") && !strings.HasPrefix(n, "+")
}

// UUID is an NVIDIA card's UUID (GPU-...), read by its PCI address. It is
// what CUDA_VISIBLE_DEVICES takes to name one card, whatever order CUDA
// counts the cards in.
func UUID(ctx context.Context, addr string) (string, error) {
	out, err := NvidiaSMI{}.run(ctx, "-i", addr, "--query-gpu=uuid", "--format=csv,noheader")
	if err != nil {
		return "", err
	}
	uuid := strings.TrimSpace(out)
	if !strings.HasPrefix(uuid, "GPU-") || strings.ContainsAny(uuid, " \n,") {
		return "", fmt.Errorf("nvidia-smi gave no UUID for %s: %q", addr, uuid)
	}
	return uuid, nil
}
