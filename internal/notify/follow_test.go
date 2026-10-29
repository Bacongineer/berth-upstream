// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

type recorder struct {
	mu    sync.Mutex
	notes []Note
}

func (r *recorder) Send(n Note) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes = append(r.notes, n)
	return nil
}

func (r *recorder) titles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, n := range r.notes {
		out = append(out, n.Title)
	}
	return out
}

// Follow skips the backlog from before it started, and after berthd
// restarts (its sequence starts over and the backlog comes again) shows
// only what is new.
func TestFollowSkipsSeenEventsAcrossARestart(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 13, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	streams := [][]proto.Event{
		{
			{Seq: 1, At: at(-60), Kind: proto.EvStuck, Engine: "old"},
			{Seq: 2, At: at(10), Kind: proto.EvRefused, Engine: "comfyui", Detail: "context canceled"},
		},
		{
			{Seq: 1, At: at(10), Kind: proto.EvRefused, Engine: "comfyui", Detail: "context canceled"},
			{Seq: 2, At: at(20), Kind: proto.EvSquatter, Engine: "", Detail: "mpv"},
			{Seq: 3, At: at(21), Kind: proto.EvGranted, Engine: "NInfer-27B"},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	opened := 0
	src := func(context.Context) (<-chan proto.Event, error) {
		mu.Lock()
		defer mu.Unlock()
		if opened == len(streams) {
			cancel()
			ch := make(chan proto.Event)
			close(ch)
			return ch, nil
		}
		ch := make(chan proto.Event, len(streams[opened]))
		for _, e := range streams[opened] {
			ch <- e
		}
		close(ch)
		opened++
		return ch, nil
	}
	r := &recorder{}
	Follow(ctx, src, nil, r, Error, t0, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))

	got := r.titles()
	want := []string{"Berth refused a request: comfyui", "GPU memory held without a lease"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("shown %q, want %q", got, want)
	}
}

// Follow asks how many cards berthd schedules and names the card in each
// note when there are several.
func TestFollowNamesCardsOnAMultiCardMachine(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := false
	src := func(context.Context) (<-chan proto.Event, error) {
		ch := make(chan proto.Event, 2)
		if !done {
			ch <- proto.Event{At: t0.Add(time.Second), Kind: proto.EvCardPassthrough, Card: "gpu0"}
			ch <- proto.Event{At: t0.Add(2 * time.Second), Kind: proto.EvCardPassthrough, Card: "gpu1"}
			done = true
		} else {
			cancel()
		}
		close(ch)
		return ch, nil
	}
	r := &recorder{}
	Follow(ctx, src, func() int { return 2 }, r, Info, t0, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got := r.titles()
	if len(got) != 2 || got[0] != "gpu0: GPU passed to a VM" || got[1] != "gpu1: GPU passed to a VM" {
		t.Fatalf("shown %q", got)
	}
}
