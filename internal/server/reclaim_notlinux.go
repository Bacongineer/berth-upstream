// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package server

// Reclaim stays list-only on this platform: there is no signal to send, so
// a confirm run records every recorded holder as skipped. Implementing it
// would drive what procs already reads on the platform.

import (
	"log/slog"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/proto"
)

func stopHolders(b *broker.Broker, hs []proto.Holder, grace time.Duration, log *slog.Logger) {
	for i, h := range hs {
		if h.Lease == "" {
			continue
		}
		hs[i].Skipped = "no signals on this platform"
	}
}
