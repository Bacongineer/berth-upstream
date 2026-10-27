// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package node is one machine's cards under one berthd. Each card keeps its
// own broker (its leases, queue, drains, parks and lends), and the node routes
// a request to the card it names, searches every card for an engine or a
// lease named by the user, and answers status and events for the machine as a
// whole. Leases never cross a machine (docs/design.md, "Many machines"), so the
// node is the one authority for its cards and the status it builds is the
// machine view a peer reads.
package node

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/eventlog"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/park"
	"github.com/T-Crypt/berth/internal/proto"
)

// Card is one governed card: its name on the node (gpu0, gpu1, in the order
// the node was given them), its PCI address in sysfs form when known, and the
// broker that schedules it.
type Card struct {
	Name string
	Addr string
	B    *broker.Broker
}

// Node is a machine's cards. The first card is the default: a request that
// names no card goes there, as every request did when a berthd had one card.
type Node struct {
	cards  []*Card
	Events *eventlog.Log  // the machine's one event stream, shared by every card's broker
	IDs    *atomic.Uint64 // the machine's lease numbers, shared by every card's broker
}

// Shared returns the event log, lease counter and parking ledger a node's
// brokers must be built with, so the machine has one ordered stream, one ID
// space and one budget of RAM for parks.
func Shared() (*eventlog.Log, *atomic.Uint64, *park.Ledger) {
	return eventlog.New(), &atomic.Uint64{}, &park.Ledger{}
}

// New is a node over cards, which must have distinct names and addresses.
func New(events *eventlog.Log, ids *atomic.Uint64, cards ...*Card) (*Node, error) {
	if len(cards) == 0 {
		return nil, errors.New("a node needs at least one card")
	}
	seen := map[string]bool{}
	for _, c := range cards {
		for _, k := range slices.Compact([]string{c.Name, c.Addr}) {
			if k == "" {
				continue
			}
			if seen[k] {
				return nil, fmt.Errorf("two cards are named %s", k)
			}
			seen[k] = true
		}
	}
	return &Node{cards: cards, Events: events, IDs: ids}, nil
}

// One is a node of one card, gpu0, over a broker built on its own: the node a
// one-card berthd serves, and the one tests serve a lone broker through.
func One(b *broker.Broker) *Node {
	return &Node{cards: []*Card{{Name: "gpu0", B: b}}, Events: b.EventLog(), IDs: b.IDs()}
}

// Cards is the node's cards in order.
func (n *Node) Cards() []*Card { return n.cards }

// Card is the card a request names: its name, or its PCI address in either
// sysfs or nvidia-smi form. "" is the first card.
func (n *Node) Card(key string) (*Card, error) {
	if key == "" {
		return n.cards[0], nil
	}
	addr := ""
	if strings.Contains(key, ":") {
		addr = gpu.SysfsAddr(key)
	}
	for _, c := range n.cards {
		if c.Name == key || (addr != "" && c.Addr == addr) {
			return c, nil
		}
	}
	names := make([]string, len(n.cards))
	for i, c := range n.cards {
		names[i] = c.Name
	}
	return nil, fmt.Errorf("no card %s on this node (cards: %s)", key, strings.Join(names, ", "))
}

// Stop drains every lease of engine on every card.
func (n *Node) Stop(engine string) ([]string, error) {
	var ids []string
	var first error
	for _, c := range n.cards {
		got, err := c.B.Stop(engine)
		if err != nil {
			first = cmpErr(first, err)
			continue
		}
		ids = append(ids, got...)
	}
	if len(ids) == 0 {
		return nil, first
	}
	slices.Sort(ids)
	return ids, nil
}

// Resume puts engine's parked lease back in line on the card it parked on.
func (n *Node) Resume(engine string, switched bool) error {
	var first error
	for _, c := range n.cards {
		err := c.B.Resume(engine, switched)
		if err == nil || !broker.IsNotFound(err) {
			return err
		}
		first = cmpErr(first, err)
	}
	return first
}

// Reclaim lists what a stuck lease recorded: the one named id on whichever
// card holds it, or, for "", every stuck lease on every card.
func (n *Node) Reclaim(id string) ([]proto.Holder, error) {
	var hs []proto.Holder
	var first error
	for _, c := range n.cards {
		got, err := c.B.Reclaim(id)
		if err != nil {
			if id != "" && !broker.IsNotFound(err) {
				return nil, err
			}
			first = cmpErr(first, err)
			continue
		}
		if id != "" {
			return got, nil
		}
		hs = append(hs, got...)
	}
	if id != "" {
		return nil, first
	}
	return hs, nil
}

// StillAlive is whether any card still sees the process.
func (n *Node) StillAlive(pid int, start uint64) bool {
	for _, c := range n.cards {
		if c.B.StillAlive(pid, start) {
			return true
		}
	}
	return false
}

// cmpErr keeps the first error it was given.
func cmpErr(first, err error) error {
	if first != nil {
		return first
	}
	return err
}

// Status is the machine's status: every card's GPU, leases, waiters and
// stowaways named by card, the board's counters added up, and what every
// card shares (the event sequence, claims, priorities, unprotected runs)
// once. Each card's status is read once, so a status costs one read per card
// however many cards the node has. With one card it is that card's broker's
// status with the card named.
func (n *Node) Status() proto.Status {
	all := make([]proto.Status, len(n.cards))
	governed := map[int]bool{}
	for i, c := range n.cards {
		all[i] = c.B.Status()
		named(&all[i], c)
		for _, g := range all[i].GPUs {
			if g.Scheduled {
				governed[g.Index] = true
			}
		}
	}
	st := all[0]
	if len(n.cards) == 1 {
		return st
	}
	// Every broker lists every device the probe sees; each card's own entry
	// comes from its broker, and a device no card governs is listed once.
	st.GPUs = slices.DeleteFunc(st.GPUs, func(g proto.GPU) bool { return !g.Scheduled && governed[g.Index] })
	runs := map[int]bool{}
	for _, r := range st.Unprotected {
		runs[r.PID] = true
	}
	foot := map[string]bool{}
	for _, f := range st.Footprints {
		foot[f.Engine] = true
	}
	var probeErrs []string
	if st.ProbeErr != "" {
		probeErrs = append(probeErrs, n.cards[0].Name+": "+st.ProbeErr)
	}
	for i, cs := range all[1:] {
		for _, g := range cs.GPUs {
			if g.Scheduled {
				st.GPUs = append(st.GPUs, g)
			}
		}
		st.Leases = append(st.Leases, cs.Leases...)
		st.Waiting = append(st.Waiting, cs.Waiting...)
		st.Squatters = append(st.Squatters, cs.Squatters...)
		for _, r := range cs.Unprotected {
			if !runs[r.PID] {
				runs[r.PID] = true
				st.Unprotected = append(st.Unprotected, r)
			}
		}
		for _, f := range cs.Footprints {
			if !foot[f.Engine] {
				foot[f.Engine] = true
				st.Footprints = append(st.Footprints, f)
			}
		}
		st.Counters = add(st.Counters, cs.Counters)
		st.ParkedBytes += cs.ParkedBytes
		if cs.ProbeErr != "" {
			probeErrs = append(probeErrs, n.cards[i+1].Name+": "+cs.ProbeErr)
		}
		if st.Passthrough == nil {
			st.Passthrough = cs.Passthrough
		}
	}
	st.ProbeErr = strings.Join(probeErrs, "; ")
	st.LastEvent = n.Events.Last()
	return st
}

// named stamps the card's name on everything in st that lives on one card.
func named(st *proto.Status, c *Card) {
	for i := range st.GPUs {
		if st.GPUs[i].Scheduled {
			st.GPUs[i].Card = c.Name
		}
	}
	for _, ls := range []([]proto.Lease){st.Leases, st.Waiting} {
		for i := range ls {
			ls[i].Card = c.Name
		}
	}
	for i := range st.Squatters {
		st.Squatters[i].Card = c.Name
	}
}

// add is the board's counters of two cards together.
func add(a, b proto.Counters) proto.Counters {
	a.CollisionsAvoided += b.CollisionsAvoided
	a.VRAMHandedOff += b.VRAMHandedOff
	a.Drains += b.Drains
	a.StuckCaught += b.StuckCaught
	a.SquattersCaught += b.SquattersCaught
	a.ContestedGrants += b.ContestedGrants
	a.Refused += b.Refused
	a.JugglesStopped += b.JugglesStopped
	if !b.Since.IsZero() && (a.Since.IsZero() || b.Since.Before(a.Since)) {
		a.Since = b.Since
	}
	return a
}
