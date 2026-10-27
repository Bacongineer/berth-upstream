// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// A parked engine answers its model list while the weights sit in RAM,
// so the gateway resumes before it probes: vLLM would otherwise see a
// request proxied to a sleeping engine. The resume queues the lease at
// its own priority; the runner wakes the engine and berthd pushes
// lease.resumed when the probe answers again. Until that event the
// gateway waits, with the same keep-alive a cold start gets.

package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// errNotParked tells the caller the engine has nothing parked, and the
// start runs as today.
var errNotParked = errors.New("engine is not parked")

// parks reports whether the engine's entry can park: park = "hook", or
// the keep_comfyui_up alias for a ComfyUI with a drain URL.
func parks(e *config.Engine) bool {
	return e.Park == "hook" || (e.KeepComfyUI && e.DrainComfyUI != "")
}

// resume puts a parked engine's lease back in the queue and waits for
// the runner's wake. Not parked is not an error: the caller starts the
// engine the usual way.
func (g *Gateway) resume(ctx context.Context, e *config.Engine, switched bool) error {
	st := g.nodeStatus()
	if st == nil {
		return errNotParked // berthd is not answering; nothing is parked
	}
	since := st.LastEvent
	conn, raw, err := server.Dial(g.Socket)
	if err != nil {
		return err
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpResume, Engine: e.Name, Switched: switched}); err != nil {
		return err
	}
	m, err := conn.Recv()
	if err != nil {
		return err
	}
	switch {
	case m.Op == proto.OpRefused:
		return errNotParked
	case m.Op != proto.OpGranted:
		return fmt.Errorf("berthd answered the resume with op %q", m.Op)
	}
	return g.waitForWake(ctx, e.Name, since)
}

// waitForWake watches the event stream until lease.resumed names the
// engine: the wake is what the gateway waits for, never the model list
// the engine answers while parked.
func (g *Gateway) waitForWake(ctx context.Context, engine string, since uint64) error {
	deadline := time.Now().Add(g.ready())
	conn, raw, err := server.Dial(g.Socket)
	if err != nil {
		return err
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpEvents, Since: since, Watch: true}); err != nil {
		return err
	}
	for {
		m, err := conn.Recv()
		if err != nil {
			return err
		}
		if m.Op == proto.OpEvent && m.Event != nil &&
			m.Event.Kind == proto.EvResumed && m.Event.Engine == engine {
			return nil
		}
		if time.Now().After(deadline) {
			return &startError{code: "not_ready",
				msg: fmt.Sprintf("%s is still waking after %s", engine, g.ready())}
		}
	}
}

// parkedIn reports whether the node's lease table shows the engine
// parked: a live answer from a parked engine is not one the gateway may
// proxy. Only the engine's own lease counts. A parked model behind a router
// (a llama-swap model under the llama-swap group) has its own front, which
// wakes it when the router forwards a request, so the router is never
// parked on its behalf.
func parkedIn(st *proto.Status, name string) bool {
	if st == nil {
		return false
	}
	for _, l := range st.Leases {
		if l.Engine == name && l.State == "parked" {
			return true
		}
	}
	return false
}
