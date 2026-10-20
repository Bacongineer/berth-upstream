// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

// berth reclaim: what a stuck lease left on the card, and at the user's
// explicit say-so, stopping exactly what the lease recorded.

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

func runReclaim(c *Context) int {
	fs := c.FlagSet()
	sock := fs.String("socket", defaultSocket(), "berthd socket ($BERTH_SOCKET)")
	yes := fs.Bool("yes", false, "stop what the lease recorded (SIGTERM, then SIGKILL after --grace)")
	grace := fs.Duration("grace", 10*time.Second, "seconds a SIGTERM has to work before SIGKILL")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) > 1 {
		return c.fail(Failf("berth reclaim takes at most one lease id").
			WithHint("usage: berth reclaim [LEASE] [--yes] [--grace D]"))
	}
	lease := ""
	if len(c.Args) == 1 {
		lease = c.Args[0]
	}
	conn, raw, err := server.Dial(*sock)
	if err != nil {
		return c.fail(Systemf("berthd unreachable on %s: %v", *sock, err).
			WithHint("nothing was signalled: reclaim runs against a live berthd"))
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpReclaim, Lease: lease, Confirm: *yes, Grace: grace.Seconds()}); err != nil {
		return c.fail(Systemf("berthd on %s: %v", *sock, err))
	}
	m, err := conn.Recv()
	if err != nil {
		return c.fail(Systemf("no reclaim reply from berthd: %v", err))
	}
	if m.Op == proto.OpRefused {
		return c.fail(Failf("%s", m.Reason))
	}
	return c.Write(reclaimView{holders: m.Holders, confirm: *yes}, nil)
}

type reclaimView struct {
	holders []proto.Holder
	confirm bool
}

func (v reclaimView) Data() any { return v.holders }

func (v reclaimView) Human(c *Context, ok bool) error {
	recorded, strangers := 0, 0
	for _, h := range v.holders {
		if h.Lease == "" {
			strangers++
		} else {
			recorded++
		}
	}
	if len(v.holders) == 0 {
		c.Printf("%s Nothing is stuck on the card\n", c.Style.OK(markOK))
		return nil
	}
	tw := tabwriter.NewWriter(c.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "LEASE\tPID\tSTART\tMEM\tCOMMAND\tRESULT")
	for _, h := range v.holders {
		lease, start, mem, result := h.Lease, h.PIDStart, server.MiB(h.Used), "-"
		if h.Used == 0 {
			mem = "-"
		}
		if h.Lease == "" {
			lease, result = c.Style.Dim("not Berth's"), c.Style.Dim("report only")
		}
		if v.confirm {
			switch {
			case h.Stopped:
				result = c.Style.OK("stopped")
			case h.Skipped != "":
				result = c.Style.Warn(h.Skipped)
			case h.Lease != "":
				result = c.Style.Warn("survived every signal")
			}
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\n", lease, h.PID, start, mem, h.Name, result)
	}
	tw.Flush()
	c.Bullet("%d %s recorded under stuck lease%s on the card", recorded, processes(recorded), plural(recorded))
	if recorded > 0 {
		if v.confirm {
			stopped, skipped := 0, 0
			for _, h := range v.holders {
				if h.Lease == "" {
					continue
				}
				if h.Stopped {
					stopped++
				} else {
					skipped++
				}
			}
			c.Bullet("%d stopped, %d not signalled; a survivor is yours to kill", stopped, skipped)
		} else {
			c.Bullet("rerun with --yes to stop them (SIGTERM, then SIGKILL after the grace)")
		}
	}
	if strangers > 0 {
		c.Bullet("%d %s on the card no lease recorded: report only, never signalled", strangers, processes(strangers))
	}
	return nil
}

// processes is "process" or "processes" for n.
func processes(n int) string {
	if n == 1 {
		return "process"
	}
	return "processes"
}
