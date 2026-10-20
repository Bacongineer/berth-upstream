// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package server

// The stopper behind a confirm run. It signals exactly what the lease
// recorded: SIGTERM to every holder still running as recorded, as long as
// grace allows, then SIGKILL to the survivors. Every signal is preceded by
// an identity re-check; a mismatch leaves that PID alone.

import (
	"log/slog"
	"syscall"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/proto"
)

func stopHolders(b *broker.Broker, hs []proto.Holder, grace time.Duration, log *slog.Logger) {
	var targets []int
	for i, h := range hs {
		if h.Lease == "" {
			continue
		}
		if h.PIDStart == 0 || !b.StillAlive(h.PID, h.PIDStart) {
			hs[i].Skipped = "no longer the process it was recorded as; not signalled"
			continue
		}
		if err := syscall.Kill(h.PID, syscall.SIGTERM); err != nil {
			hs[i].Skipped = "sigterm failed: " + err.Error()
			continue
		}
		targets = append(targets, i)
	}
	if len(targets) == 0 {
		return
	}
	gone := func() bool {
		for _, i := range targets {
			if b.StillAlive(hs[i].PID, hs[i].PIDStart) {
				return false
			}
		}
		return true
	}
	for deadline := time.Now().Add(grace); !gone() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	for _, i := range targets {
		if b.StillAlive(hs[i].PID, hs[i].PIDStart) {
			if err := syscall.Kill(hs[i].PID, syscall.SIGKILL); err != nil {
				hs[i].Skipped = "sigkill failed: " + err.Error()
			}
		}
	}
	for deadline := time.Now().Add(5 * time.Second); !gone() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	for _, i := range targets {
		if b.StillAlive(hs[i].PID, hs[i].PIDStart) {
			log.Warn("reclaim: holder survived every signal", "pid", hs[i].PID, "lease", hs[i].Lease)
			continue
		}
		hs[i].Stopped = true
	}
}
