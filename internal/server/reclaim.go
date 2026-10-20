// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

// OpReclaim. A list-only reply names the card processes in question; with
// Confirm the recorded ones are stopped exactly as the user asked, and the
// reply says per holder what happened. Nothing here touches a process no
// lease recorded.

import (
	"log/slog"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/proto"
)

// defaultGrace is how long a SIGTERM has to work before the stopper
// escalates to SIGKILL; --grace names it from the CLI.
const defaultGrace = 10 * time.Second

func reclaim(conn *proto.Conn, log *slog.Logger, req proto.Msg, b *broker.Broker) {
	hs, err := b.Reclaim(req.Lease)
	if err != nil {
		reply(conn, log, proto.Msg{Op: proto.OpRefused, Reason: err.Error()})
		return
	}
	if req.Confirm {
		grace := defaultGrace
		if req.Grace > 0 {
			grace = time.Duration(req.Grace * float64(time.Second))
		}
		stopHolders(b, hs, grace, log)
	}
	if hs == nil {
		hs = []proto.Holder{}
	}
	reply(conn, log, proto.Msg{Op: proto.OpReclaim, Holders: hs})
}
