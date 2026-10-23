// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The run's front (berth run --front) and the checkpoint park it makes
// safe. docs/design.md, "Fronts", has the why: an engine behind a router
// Berth does not own (a llama-swap model) may only sleep when every
// request to it passes through a door that holds the request and wakes
// the engine first.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/checkpoint"
	"github.com/T-Crypt/berth/internal/front"
	"github.com/T-Crypt/berth/internal/park"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// PortPlaceholder is where an engine behind a front takes the port the run
// picks for it: `--port {{BERTH_PORT}}` in the engine's command.
const PortPlaceholder = "{{BERTH_PORT}}"

// openFront listens on o.Front and points the engine at a free loopback
// port, written into its command at PortPlaceholder. It returns the
// options the run continues with and the stop for the front's server.
func openFront(o Options) (Options, func(), error) {
	if !slices.ContainsFunc(o.Argv, func(a string) bool { return strings.Contains(a, PortPlaceholder) }) {
		return o, nil, fmt.Errorf("--front needs %s in the engine's command, where its port goes", PortPlaceholder)
	}
	ln, err := listenSoon(o.Front, 10*time.Second)
	if err != nil {
		return o, nil, fmt.Errorf("front %s: %w", o.Front, err)
	}
	port, err := freePort()
	if err != nil {
		ln.Close()
		return o, nil, fmt.Errorf("a port for the engine behind the front: %w", err)
	}
	argv := make([]string, len(o.Argv))
	for i, a := range o.Argv {
		argv[i] = strings.ReplaceAll(a, PortPlaceholder, strconv.Itoa(port))
	}
	o.Argv = argv
	engine := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	o.ParkBase = engine.String()
	hold := o.Wait
	if hold <= 0 {
		hold = 10 * time.Minute
	}
	sock, name := o.Socket, o.Engine
	o.front = front.New(engine, func(ctx context.Context) error { return askResume(ctx, sock, name) }, hold)
	srv := &http.Server{Handler: o.front, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return o, func() { _ = srv.Close() }, nil
}

// listenSoon listens on addr, trying again for up to within: a router that
// restarts a model hands the new run the port the old one is still
// letting go of.
func listenSoon(addr string, within time.Duration) (net.Listener, error) {
	deadline := time.Now().Add(within)
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || time.Now().After(deadline) {
			return ln, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// freePort is a loopback port free right now, for the engine to bind.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// askResume asks berthd to give the engine's parked lease the card back,
// as berth resume does. It returns once the ask is queued; a refusal
// (the lease is not parked yet, or berthd is down) comes back as the
// error the front retries on.
func askResume(ctx context.Context, sock, engine string) error {
	conn, raw, err := server.Dial(sock)
	if err != nil {
		return fmt.Errorf("berthd unreachable: %w", err)
	}
	defer raw.Close()
	if d, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(d)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpResume, Engine: engine}); err != nil {
		return err
	}
	m, err := conn.Recv()
	if err != nil {
		return err
	}
	if m.Op == proto.OpRefused {
		return errors.New(m.Reason)
	}
	return nil
}

// checkpointPark is the park adapter for an engine with no offload call of
// its own: cuda-checkpoint moves the engine's device memory into its own
// RAM. The drain has already closed the front, so nothing is in flight.
// The tool is looked up at each park, so installing it needs no restart;
// without it the park fails, berthd puts the why on the event stream, and
// the drain stops the engine as it always has.
func checkpointPark(o Options, pid int, say func(string, ...any)) *park.Adapter {
	if _, err := checkpoint.Find(); err != nil {
		say("%v; until it is there, a drain stops %s instead of parking it", err, o.Engine)
	}
	return &park.Adapter{
		Park: func(ctx context.Context, _ string) error {
			tool, err := checkpoint.Find()
			if err != nil {
				return err
			}
			// A failed park leaves the front closed: the stop ladder runs
			// next, and the held requests end with the engine.
			return checkpoint.Suspend(ctx, tool, pid, o.Grace)
		},
		Resume: func(ctx context.Context, _ string) error {
			tool, err := checkpoint.Find()
			if err != nil {
				return err
			}
			return checkpoint.Restore(ctx, tool, pid)
		},
	}
}
