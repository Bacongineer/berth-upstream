// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

const gib = 1 << 30

// card is one fake device: what the driver reports for it.
type card struct {
	mu    sync.Mutex
	index int
	total uint64
	procs []gpu.Proc
}

func (c *card) Read(context.Context) (gpu.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var used uint64
	for _, p := range c.procs {
		used += p.Used
	}
	return gpu.Snapshot{Index: c.index, Total: c.total, Used: used, Free: c.total - used,
		Procs: append([]gpu.Proc(nil), c.procs...)}, nil
}

type quiet struct{}

func (quiet) Squatter(bool, gpu.Proc)      {}
func (quiet) Alert(string, string, string) {}

// twoCards is a node over gpu0 (0000:01:00.0) and gpu1 (0000:02:00.0), 24 GiB
// each, with the shared event log, lease counter and parking ledger.
func twoCards(t *testing.T) *Node {
	t.Helper()
	events, ids, seq, ledger := Shared()
	var cards []*Card
	for i, addr := range []string{"0000:01:00.0", "0000:02:00.0"} {
		name := []string{"gpu0", "gpu1"}[i]
		b := broker.New(broker.Config{
			Card: name, Events: events, IDs: ids, Seq: seq, Ledger: ledger, Reporter: quiet{},
			ReleaseGrace: time.Hour, DefaultWait: 2 * time.Second, JuggleWindow: -1,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}, &card{index: i, total: 24 * gib}, procs.Map{})
		b.Tick(context.Background())
		cards = append(cards, &Card{Name: name, Addr: addr, B: b})
	}
	n, err := New(events, ids, cards...)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func grant(t *testing.T, n *Node, cardKey, engine string) *broker.Lease {
	t.Helper()
	c, err := n.Card(cardKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	l, err := c.B.Acquire(ctx, broker.Request{Engine: engine, Priority: broker.Foreground}, nil)
	if err != nil {
		t.Fatalf("acquire %s on %s: %v", engine, cardKey, err)
	}
	return l
}

// A request names its card by name or by PCI address in either form; no
// card means the first, as every request meant on a one-card berthd.
func TestCardIsFoundByNameOrAddress(t *testing.T) {
	n := twoCards(t)
	for key, want := range map[string]string{
		"": "gpu0", "gpu1": "gpu1", "0000:02:00.0": "gpu1", "00000000:02:00.0": "gpu1", "0000:01:00.0": "gpu0",
	} {
		c, err := n.Card(key)
		if err != nil || c.Name != want {
			t.Fatalf("Card(%q) = %v, %v; want %s", key, c, err, want)
		}
	}
	if _, err := n.Card("gpu7"); err == nil || err.Error() != "no card gpu7 on this node (cards: gpu0, gpu1)" {
		t.Fatalf("Card(gpu7) error = %v, want the node's cards named", err)
	}
}

// Two cards each grant a whole-card foreground lease at once: one card's
// holder never makes the other card's request wait. The two leases of one
// engine get different IDs, so a stop or a reclaim names one of them.
func TestCardsScheduleApartAndLeaseIDsStayUnique(t *testing.T) {
	n := twoCards(t)
	a := grant(t, n, "gpu0", "strata")
	b := grant(t, n, "gpu1", "strata")
	if a.ID() == b.ID() {
		t.Fatalf("both cards' leases are %s; want one ID per lease on the node", a.ID())
	}
	st := n.Status()
	got := map[string]string{}
	for _, l := range st.Leases {
		got[l.ID] = l.Card
	}
	if got[a.ID()] != "gpu0" || got[b.ID()] != "gpu1" || len(got) != 2 {
		t.Fatalf("status leases = %v, want %s on gpu0 and %s on gpu1", got, a.ID(), b.ID())
	}
}

// Every card writes to one event stream: the sequence numbers run in order
// across cards, and each event names its card.
func TestEventsAreOneStreamNamedByCard(t *testing.T) {
	n := twoCards(t)
	grant(t, n, "gpu0", "comfyui")
	grant(t, n, "gpu1", "vllm")

	evs, _, _ := n.Events.Events(0, false)
	cards := map[string]string{}
	for i, e := range evs {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d; want one sequence for the node", i, e.Seq)
		}
		if e.Kind == proto.EvGranted {
			cards[e.Engine] = e.Card
		}
	}
	if cards["comfyui"] != "gpu0" || cards["vllm"] != "gpu1" {
		t.Fatalf("granted events by card = %v, want comfyui on gpu0 and vllm on gpu1", cards)
	}
	if st := n.Status(); st.LastEvent != uint64(len(evs)) {
		t.Fatalf("status last event = %d, want %d", st.LastEvent, len(evs))
	}
}

// The machine's status lists each card's GPU once, by card name.
func TestStatusListsEachCardOnce(t *testing.T) {
	n := twoCards(t)
	st := n.Status()
	var cards []string
	for _, g := range st.GPUs {
		cards = append(cards, g.Card)
	}
	if strings.Join(cards, ",") != "gpu0,gpu1" {
		t.Fatalf("status GPUs = %v, want gpu0,gpu1", cards)
	}
}

// berth stop and berth resume name an engine, not a card: the node finds the
// card it runs on, and a name no card knows gets the one-card answer.
func TestStopAndResumeFindTheEngineOnItsCard(t *testing.T) {
	n := twoCards(t)
	l := grant(t, n, "gpu1", "vllm")
	l.Attach(4242)
	ids, err := n.Stop("vllm")
	if err != nil || len(ids) != 1 || ids[0] != l.ID() {
		t.Fatalf("Stop(vllm) = %v, %v; want %s on gpu1", ids, err, l.ID())
	}
	if _, err := n.Stop("nope"); err == nil || err.Error() != "no running engine named nope under a lease" {
		t.Fatalf("Stop(nope) error = %v", err)
	}
	if err := n.Resume("nope", false); err == nil || err.Error() != "nope is not parked" {
		t.Fatalf("Resume(nope) error = %v", err)
	}
}

// A card in a VM at startup is named by its address, and that is one card,
// not two cards with the same name.
func TestNewTakesACardNamedByItsAddress(t *testing.T) {
	events, ids, _, _ := Shared()
	b := broker.New(broker.Config{Events: events, IDs: ids}, &card{total: 24 * gib}, procs.Map{})
	n, err := New(events, ids, &Card{Name: "0000:02:00.0", Addr: "0000:02:00.0", B: b})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := n.Card("00000000:02:00.0"); err != nil || c.Name != "0000:02:00.0" {
		t.Fatalf("card by nvidia-smi address = %v, %v", c, err)
	}
}
