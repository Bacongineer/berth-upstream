// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

// berth stop: a person stops an engine on purpose. The daemon asks every
// active or draining lease of the engine to give the card back; each
// lease's wrapper runs the normal stop ladder, and the lease frees only
// after a card read shows its memory gone. Engines Berth never leased are
// not Berth's to stop.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

func runStop(c *Context) int {
	fs := c.FlagSet()
	sock := fs.String("socket", defaultSocket(), "berthd socket ($BERTH_SOCKET)")
	wait := fs.Bool("wait", false, "follow the event log until every asked lease is freed or stuck")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Failf("berth stop takes one engine name").
			WithHint("usage: berth stop ENGINE [--wait]"))
	}
	engine := c.Args[0]
	conn, raw, err := server.Dial(*sock)
	if err != nil {
		return c.fail(Systemf("berthd unreachable on %s: %v", *sock, err).
			WithHint("nothing was drained: stop runs against a live berthd"))
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpStop, Engine: engine}); err != nil {
		return c.fail(Systemf("berthd on %s: %v", *sock, err))
	}
	m, err := conn.Recv()
	if err != nil {
		return c.fail(Systemf("no stop reply from berthd: %v", err))
	}
	if m.Op == proto.OpRefused {
		return c.fail(Failf("%s", m.Reason))
	}
	leases := m.Leases
	if leases == nil {
		leases = []string{}
	}
	v := stopView{engine: engine, leases: leases}
	if !*wait {
		return c.Write(v, nil)
	}
	freed, stuck, e := waitOutcomes(*sock, engine, leases)
	if e != nil {
		return c.fail(e)
	}
	v.freed, v.stuck = freed, stuck
	if len(stuck) > 0 {
		return c.Write(v, Systemf("stuck: %s", strings.Join(stuck, ", ")).WithHint("%s", reclaimHint(stuck)))
	}
	return c.Write(v, nil)
}

// reclaimHint says what to run for the stuck leases: one names the lease,
// many cover every stuck lease at once.
func reclaimHint(stuck []string) string {
	if len(stuck) == 1 {
		return fmt.Sprintf("berth reclaim %s --yes stops what the lease recorded", stuck[0])
	}
	return "berth reclaim --yes stops what the stuck leases recorded"
}

// waitOutcomes follows the daemon's event log until every asked lease is
// freed or stuck. The ring replays the events of the ask, so one that freed
// before this connection came up is still seen. A watcher cut off for
// falling behind is a system failure, like any broken link to the daemon.
func waitOutcomes(sock, engine string, leases []string) (freed, stuck []string, err *Error) {
	conn, raw, dialErr := server.Dial(sock)
	if dialErr != nil {
		return nil, nil, Systemf("berthd unreachable on %s: %v", sock, dialErr)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpEvents, Watch: true}); err != nil {
		return nil, nil, Systemf("berthd on %s: %v", sock, err)
	}
	want := make(map[string]bool, len(leases))
	for _, id := range leases {
		want[id] = true
	}
	freedSet, stuckSet := map[string]bool{}, map[string]bool{}
	for len(freedSet)+len(stuckSet) < len(leases) {
		m, err := conn.Recv()
		if err != nil {
			return nil, nil, Systemf("berthd went away while following %s: %v", engine, err)
		}
		if m.Op == proto.OpRefused {
			return nil, nil, Systemf("%s", m.Reason)
		}
		e := m.Event
		if e == nil || !want[e.Lease] {
			continue
		}
		switch e.Kind {
		case proto.EvFreed:
			freedSet[e.Lease] = true
		case proto.EvStuck:
			// A stuck lease is settled here: it stays stuck until
			// someone reclaims it, and that is the next step, not
			// this command's.
			stuckSet[e.Lease] = true
		}
	}
	for id := range freedSet {
		freed = append(freed, id)
	}
	for id := range stuckSet {
		stuck = append(stuck, id)
	}
	sort.Strings(freed)
	sort.Strings(stuck)
	return freed, stuck, nil
}

type stopView struct {
	engine string
	leases []string // the leases asked to drain
	freed  []string // --wait: the ones the card showed gone
	stuck  []string // --wait: the ones still holding memory after release
}

func (v stopView) Data() any {
	return struct {
		Engine string   `json:"engine"`
		Leases []string `json:"leases"`
		Freed  []string `json:"freed,omitempty"`
		Stuck  []string `json:"stuck,omitempty"`
	}{v.engine, v.leases, v.freed, v.stuck}
}

func (v stopView) Human(c *Context, ok bool) error {
	if len(v.leases) == 0 {
		return nil
	}
	c.Printf("%s: %d lease%s asked to drain: %s\n", v.engine, len(v.leases), plural(len(v.leases)), strings.Join(v.leases, ", "))
	if len(v.freed)+len(v.stuck) == 0 {
		c.Bullet("the engine stops through its own wrapper (its job boundary, then the signals)")
		c.Bullet("the lease frees when the card shows its memory gone; --wait follows until then")
		return nil
	}
	if len(v.freed) > 0 {
		c.Bullet("%s freed: %s", c.Style.OK("freed"), strings.Join(v.freed, ", "))
	}
	if len(v.stuck) > 0 {
		c.Bullet("%s stuck: %s", c.Style.Err("stuck"), strings.Join(v.stuck, ", "))
	}
	return nil
}
