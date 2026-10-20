// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// The runner's side of parking. One lease's connection is the line the
// park protocol rides: on a drain the runner asks the broker for
// admission, runs the adapter's park, and holds the lease with the
// engine asleep in RAM; a grant with Resume set wakes the engine again
// and waits for its readiness probe. Eviction and resume arrive on the
// same conn, so the line lives as long as the lease does.
package runner

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/T-Crypt/berth/internal/park"
	"github.com/T-Crypt/berth/internal/proto"
)

// The park protocol's windows. The broker's admission answer is one call
// away; the adapter calls carry the engines' own cost.
const (
	parkReplyWait   = 30 * time.Second // how long the broker has to answer admission
	parkCallTimeout = 60 * time.Second // the adapter moves the weights once
	wakeCallTimeout = 120 * time.Second
	parkCadence     = time.Second // the resume probe's cadence
)

// leaseLine is one lease's park side: the connection the lease rides
// (rebuilt across reattach), the daemon's park replies, whether the
// engine is parked now, and the one protocol probe a hook without
// adapter URLs is allowed.
type leaseLine struct {
	mu      sync.Mutex
	conn    *proto.Conn
	replies chan proto.Msg
	parked  bool
	probed  *parkVerdict
}

func newLine(conn *proto.Conn) *leaseLine {
	return &leaseLine{conn: conn, replies: make(chan proto.Msg, 1)}
}

func (l *leaseLine) isParked() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.parked
}

func (l *leaseLine) setConn(conn *proto.Conn) {
	l.mu.Lock()
	l.conn = conn
	l.mu.Unlock()
}

func (l *leaseLine) send(m proto.Msg) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conn == nil {
		return errors.New("no connection to berthd")
	}
	return l.conn.Send(m)
}

// clear takes a reply an earlier drain left behind, so a new park starts
// on an empty line.
func (l *leaseLine) clear() {
	for {
		select {
		case <-l.replies:
		default:
			return
		}
	}
}

// note takes the park replies the drain waits on; the runner routes them
// as they arrive.
func (l *leaseLine) note(m proto.Msg) {
	select {
	case l.replies <- m:
	default:
	}
}

func (l *leaseLine) waitReply() (*proto.Msg, bool) {
	t := time.NewTimer(parkReplyWait)
	defer t.Stop()
	select {
	case m := <-l.replies:
		return &m, true
	case <-t.C:
		return nil, false
	}
}

// adapterFor is the park adapter for this run: out options, or the
// protocol verdict the first drain recorded. Before a drain has asked,
// a hook without URLs has none yet.
func (l *leaseLine) adapterFor(o Options) (park.Adapter, bool) {
	if o.Park != nil {
		return *o.Park, true
	}
	l.mu.Lock()
	probed := l.probed
	l.mu.Unlock()
	if probed == nil || !probed.ok {
		return park.Adapter{}, false
	}
	return probed.adapter, true
}

// adapter is the park adapter the park step asks through: out options,
// or Berth's own park protocol, probed once the first time a drain would
// ask. A hook whose engine does not speak the protocol answers false
// once, and every later drain runs today's ladder without asking again.
func (l *leaseLine) adapter(o Options, say func(string, ...any)) (park.Adapter, bool) {
	if o.Park != nil {
		return *o.Park, true
	}
	if !o.Hook {
		return park.Adapter{}, false
	}
	l.mu.Lock()
	probed := l.probed
	l.mu.Unlock()
	if probed == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		adapter, ok := park.Protocol(ctx, o.ParkBase)
		cancel()
		probed = &parkVerdict{adapter: adapter, ok: ok}
		l.mu.Lock()
		l.probed = probed
		l.mu.Unlock()
		if !ok {
			say("%s does not speak Berth's park protocol; draining the usual way", o.ParkBase)
		}
	}
	return probed.adapter, probed.ok
}

// parkVerdict remembers the one probe a hook without URLs is allowed.
type parkVerdict struct {
	adapter park.Adapter
	ok      bool
}

// park takes the engine off the card on a drain: the broker's admission,
// the adapter's call, the reservation on record. A refusal or a failed
// call says so and leaves the lease on the stop ladder.
func (l *leaseLine) park(o Options, say func(string, ...any)) bool {
	if l.isParked() {
		return false // this drain evicts; the engine stops instead
	}
	l.clear()
	adapter, ok := l.adapter(o, say)
	if !ok {
		return false
	}
	if err := l.send(proto.Msg{Op: proto.OpPark}); err != nil {
		say("park request unreachable (%v); draining the usual way", err)
		return false
	}
	reply, ok := l.waitReply()
	if !ok {
		say("berthd never answered the park request; draining the usual way")
		_ = l.send(proto.Msg{Op: proto.OpParkFailed})
		return false
	}
	if reply.Op != proto.OpParkOK {
		say("park refused: %s", reply.Reason)
		return false
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), parkCallTimeout)
	err := adapter.Park(ctx, o.ParkBase)
	cancel()
	if err != nil {
		say("park call failed (%v); draining the usual way", err)
		_ = l.send(proto.Msg{Op: proto.OpParkFailed})
		return false
	}
	if err := l.send(proto.Msg{Op: proto.OpParked}); err != nil {
		say("parked, but could not reach berthd: %v", err)
	}
	l.mu.Lock()
	l.parked = true
	l.mu.Unlock()
	say("parked %s in RAM in %s", o.Engine, time.Since(start).Round(100*time.Millisecond))
	return true
}

// resume drives the wake-up the runner hears as a grant with Resume set:
// the adapter's call, the readiness probe, the engine serving again. A
// wake that never answers stops the engine, which hands the card back:
// a wedged engine must not keep the lease.
func (l *leaseLine) resume(o Options, cmd *exec.Cmd, done <-chan struct{}, say func(string, ...any)) {
	if !l.isParked() {
		return
	}
	l.mu.Lock()
	l.parked = false
	l.mu.Unlock()
	start := time.Now()
	adapter, hasWake := l.adapterFor(o)
	if hasWake && adapter.Resume != nil {
		say("resuming %s", o.Engine)
		ctx, cancel := context.WithTimeout(context.Background(), wakeCallTimeout)
		err := adapter.Resume(ctx, o.ParkBase)
		cancel()
		if err != nil {
			say("resuming %s failed (%v); stopping it", o.Engine, err)
			stopNow(o, cmd, done, say)
			return
		}
	}
	if !o.parkReady() {
		say("%s never answered after the wake; stopping it", o.Engine)
		stopNow(o, cmd, done, say)
		return
	}
	say("resumed %s in %s", o.Engine, time.Since(start).Round(100*time.Millisecond))
	if err := l.send(proto.Msg{Op: proto.OpAwake, Lease: o.Engine}); err != nil {
		say("resumed, but could not tell berthd (%v)", err)
	}
}

// parkReady waits for the engine's readiness path (the model list for
// LLMs, the device stats for comfy) after the wake, until the window's
// end.
func (o Options) parkReady() bool {
	deadline := time.Now().Add(wakeCallTimeout)
	url := strings.TrimRight(o.ParkBase, "/") + o.ParkProbe
	for {
		if askReady(url) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(parkCadence)
	}
}

func askReady(url string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// stopNow runs the stop ladder's tail: terminate, then kill after Grace.
// The engine exits, the wait returns, and the lease is released with it.
func stopNow(o Options, cmd *exec.Cmd, done <-chan struct{}, say func(string, ...any)) {
	terminate(cmd)
	select {
	case <-done:
		return
	case <-time.After(o.Grace):
	}
	say("%s ignored terminate for %s; killing", o.Engine, o.Grace)
	kill(cmd)
}
