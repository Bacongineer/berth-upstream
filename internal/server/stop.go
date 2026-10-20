// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

// OpStop. The broker asks the named engine's leases to drain and the
// reply names them. Nothing here signals a process: each lease's own
// wrapper runs the stop ladder.

import (
	"log/slog"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/proto"
)

func stop(conn *proto.Conn, log *slog.Logger, req proto.Msg, b *broker.Broker) {
	if req.Engine == "" {
		reply(conn, log, proto.Msg{Op: proto.OpRefused, Reason: "engine name is required"})
		return
	}
	ids, err := b.Stop(req.Engine)
	if err != nil {
		reply(conn, log, proto.Msg{Op: proto.OpRefused, Reason: err.Error()})
		return
	}
	if ids == nil {
		ids = []string{}
	}
	reply(conn, log, proto.Msg{Op: proto.OpStop, Engine: req.Engine, Leases: ids})
}
