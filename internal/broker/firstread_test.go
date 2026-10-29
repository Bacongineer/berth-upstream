// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
)

// downProbe fails every read until up is set.
type downProbe struct {
	mu sync.Mutex
	up bool
}

func (p *downProbe) Read(context.Context) (gpu.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.up {
		return gpu.Snapshot{}, errors.New("nvidia-smi: unable to determine the device handle")
	}
	return gpu.Snapshot{Total: 24 * GiB, Free: 24 * GiB, At: time.Now(), Devices: []gpu.Device{{Total: 24 * GiB, Free: 24 * GiB, Name: "Fake"}}}, nil
}

// A card berthd has never read grants nothing: one card of several can fail
// its first read while berthd serves the rest, and a grant from the lease
// table alone would count a card of unknown size as empty. The first good
// read lets the request in.
func TestCardNeverReadWaitsForItsFirstRead(t *testing.T) {
	p := &downProbe{}
	b := New(Config{ReleaseGrace: time.Hour, DefaultWait: 5 * time.Second, JuggleWindow: -1,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, p, procs.Map{})
	b.Tick(context.Background())

	ch := acquire(b, Request{Engine: "chat"}) // the whole card
	b.Tick(context.Background())
	pending(t, ch)
	if why := lastWaiting(b); !strings.Contains(why, "first GPU observation") {
		t.Fatalf("waiting reason %q, want the first observation", why)
	}

	p.mu.Lock()
	p.up = true
	p.mu.Unlock()
	b.Tick(context.Background())
	must(t, ch).Release()
}
