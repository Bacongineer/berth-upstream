// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

// OpStop through the real socket and wrapper: the named engine's lease is
// asked to drain, its wrapper runs the stop ladder and signals the
// process, and the lease frees only after a card read shows it gone.

import (
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/runner"
	"github.com/T-Crypt/berth/internal/server"
)

func TestStopEndToEnd(t *testing.T) {
	sock := start(t)
	code, out := launch(runner.Options{Socket: sock, Engine: "ninfer", Grace: 200 * time.Millisecond, Argv: []string{"sleep", "30"}})
	waitFor(t, "grant", func() bool { return strings.Contains(out.String(), "granted") })

	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpStop, Engine: "ninfer"}); err != nil {
		t.Fatal(err)
	}
	m, err := conn.Recv()
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if m.Op != proto.OpStop || m.Engine != "ninfer" || len(m.Leases) != 1 || !strings.HasPrefix(m.Leases[0], "ninfer-") {
		t.Fatalf("stop reply = %+v, want one ninfer lease", m)
	}
	select {
	case c := <-code:
		if c != 128+15 {
			t.Fatalf("engine exit = %d, want 143 (SIGTERM); stderr:\n%s", c, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("engine was not stopped; stderr:\n%s", out.String())
	}
	waitFor(t, "lease freed", func() bool {
		c, r, err := server.Dial(sock)
		if err != nil {
			return false
		}
		defer r.Close()
		if err := c.Send(proto.Msg{Op: proto.OpStatus}); err != nil {
			return false
		}
		m, err := c.Recv()
		return err == nil && m.Status != nil && len(m.Status.Leases) == 0
	})
}

// An engine no attached lease answers to is refused, named, and nothing is
// asked.
func TestStopUnknownEngine(t *testing.T) {
	sock := start(t)
	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpStop, Engine: "nobody"}); err != nil {
		t.Fatal(err)
	}
	m, err := conn.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if m.Op != proto.OpRefused || !strings.Contains(m.Reason, "no running engine named nobody under a lease") {
		t.Fatalf("reply = %+v, want the refusal naming the engine", m)
	}
}

func TestStopRefusesANoneName(t *testing.T) {
	sock := start(t)
	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpStop}); err != nil {
		t.Fatal(err)
	}
	m, err := conn.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if m.Op != proto.OpRefused || m.Reason != "engine name is required" {
		t.Fatalf("reply = %+v, want the name refusal", m)
	}
}
