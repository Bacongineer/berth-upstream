// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

// berth resume: put a parked engine back in the queue at its own priority.

import (
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

type resumeView struct {
	engine string
	lease  string
}

func (v resumeView) Data() any {
	return struct {
		Engine string `json:"engine" yaml:"engine"`
		Lease  string `json:"lease,omitempty" yaml:"lease,omitempty"`
	}{v.engine, v.lease}
}

func (v resumeView) Human(c *Context, ok bool) error {
	c.Printf("%s %s is queued for the card at its own priority\n", c.Style.OK(markOK), v.engine)
	if v.lease != "" {
		c.Bullet("the runner on %s hears the grant and calls the adapter's resume", v.lease)
	}
	return nil
}

func runResume(c *Context) int {
	fs := c.FlagSet()
	sock := fs.String("socket", defaultSocket(), "berthd socket ($BERTH_SOCKET)")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("berth resume takes exactly one engine name").
			WithHint("usage: berth resume ENGINE"))
	}
	engine := c.Args[0]
	conn, raw, err := server.Dial(*sock)
	if err != nil {
		return c.fail(Systemf("berthd unreachable on %s: %v", *sock, err))
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpResume, Engine: engine}); err != nil {
		return c.fail(Systemf("berthd on %s: %v", *sock, err))
	}
	m, err := conn.Recv()
	if err != nil {
		return c.fail(Systemf("no reply from berthd: %v", err))
	}
	if m.Op == proto.OpRefused {
		return c.fail(Failf("%s", m.Reason))
	}
	if m.Op != proto.OpGranted {
		return c.fail(Systemf("unexpected reply from berthd: op %q", m.Op))
	}
	return c.Write(resumeView{engine: engine, lease: m.Lease}, nil)
}
