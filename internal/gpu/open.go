// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// ErrUnsafeRead means a probe cannot establish a trustworthy observation.
// Unlike the historical NVIDIA fallback, it must block new admissions.
var ErrUnsafeRead = errors.New("GPU observation unavailable")

// Options selects a backend and its card. AMD indices are DRM card numbers,
// NVIDIA indices are nvidia-smi indices; neither is a HIP/CUDA ordinal.
type Options struct {
	Backend  string
	Index    int
	Interval time.Duration
	Log      *slog.Logger
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

// Open reuses Probe for both vendors. Auto refuses an ambiguous mixed-vendor
// host rather than silently choosing a different vendor's card zero.
func Open(o Options) (Probe, error) {
	if err := ValidateSelection(o.Backend, o.Index); err != nil {
		return nil, err
	}
	backend := o.Backend
	if backend == "" || backend == "auto" {
		amd, err := hasAMD(o)
		if err != nil {
			return nil, err
		}
		_, nvErr := exec.LookPath("nvidia-smi")
		switch {
		case amd && nvErr == nil:
			return nil, fmt.Errorf("AMD GPUs and nvidia-smi found; select --gpu-backend amd or nvidia explicitly")
		case amd:
			backend = "amd"
		case nvErr == nil:
			backend = "nvidia"
		default:
			return nil, fmt.Errorf("no supported GPU backend: need nvidia-smi or Linux amdgpu with an RDNA3/RDNA4 discrete GPU or Phoenix APU")
		}
	}
	if backend == "amd" {
		return openAMD(o)
	}
	if o.Interval > 0 {
		return &Stream{Index: o.Index, Interval: o.Interval, Log: o.Log}, nil
	}
	return NvidiaSMI{Index: o.Index, Log: o.Log}, nil
}
