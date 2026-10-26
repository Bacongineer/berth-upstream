// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

// OpResume: any client (the gateway, berth resume) puts a parked engine
// back in the queue at its own priority. The resuming connection gets one
// reply; the runner's own connection hears OpGranted{Resume: true} when the
// grant lands.

import (
	"log/slog"

	"github.com/T-Crypt/berth/internal/node"
	"github.com/T-Crypt/berth/internal/proto"
)

func resume(conn *proto.Conn, log *slog.Logger, req proto.Msg, n *node.Node) {
	err := n.Resume(req.Engine, req.Switched)
	switch {
	case err == nil:
		reply(conn, log, proto.Msg{Op: proto.OpGranted, Engine: req.Engine, Reason: "queued at its own priority"})
	case req.Engine == "":
		reply(conn, log, proto.Msg{Op: proto.OpRefused, Reason: "engine name is required"})
	default:
		reply(conn, log, proto.Msg{Op: proto.OpRefused, Reason: err.Error()})
	}
}
