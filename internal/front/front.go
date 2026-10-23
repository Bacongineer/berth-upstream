// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package front is the one door to an engine whose router Berth does not
// own (a llama-swap model): it forwards requests while the engine is
// awake and holds them while it sleeps, so a request never reaches an
// engine whose memory is in RAM. docs/design.md, "Fronts", has the why.
package front

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// state is where the front stands with its engine.
type state int

const (
	open    state = iota // forwarding
	closing              // holding new requests while the in-flight ones finish
	asleep               // holding; the first request asks for the card
	waking               // holding; the card has been asked for
)

// Front forwards to one engine and holds requests while it sleeps.
type Front struct {
	proxy *httputil.ReverseProxy
	// wake asks berthd to give the engine the card back. It returns once
	// the ask is queued; the engine is awake when the run calls Open.
	wake func(context.Context) error
	// hold is the longest a request waits for a sleeping engine.
	hold time.Duration
	// retry is how often a refused wake is asked again.
	retry time.Duration

	mu       sync.Mutex
	state    state
	inflight int
	changed  chan struct{} // closed and replaced on every change of state or in-flight count
	why      string        // the last refused wake, named in a held request's 503
}

// New is a front for the engine at target, open.
func New(target *url.URL, wake func(context.Context) error, hold time.Duration) *Front {
	p := httputil.NewSingleHostReverseProxy(target)
	p.FlushInterval = -1 // stream tokens as the engine sends them
	return &Front{proxy: p, wake: wake, hold: hold, retry: 500 * time.Millisecond, changed: make(chan struct{})}
}

// set moves to s and wakes everything waiting on a change. f.mu must be
// held.
func (f *Front) set(s state) {
	f.state = s
	f.broadcast()
}

func (f *Front) broadcast() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// ServeHTTP forwards r while the engine is awake, and otherwise holds it
// until the engine wakes or the hold runs out. A read (GET, HEAD,
// OPTIONS) never wakes the engine: monitors poll it every few seconds,
// Berth's own engine reader among them, and each poll would undo the
// sleep. While the engine sleeps a read gets a 503 that says so.
func (f *Front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wait, cancel := context.WithTimeout(r.Context(), f.hold)
	defer cancel()
	read := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
	for {
		f.mu.Lock()
		if f.state != open && read {
			f.mu.Unlock()
			w.Header().Set("Retry-After", "5")
			http.Error(w, "the engine is asleep in RAM; a read does not wake it", http.StatusServiceUnavailable)
			return
		}
		if f.state == open {
			f.inflight++
			f.mu.Unlock()
			// The proxy panics with http.ErrAbortHandler when the client
			// hangs up mid-stream: the count drops either way.
			defer f.finished()
			f.proxy.ServeHTTP(w, r)
			return
		}
		if f.state == asleep {
			f.set(waking)
			go f.wakeLoop()
		}
		ch, why := f.changed, f.why
		f.mu.Unlock()
		select {
		case <-ch:
		case <-wait.Done():
			msg := "the engine is asleep in RAM and Berth has not given it the card back yet"
			if why != "" {
				msg += ": " + why
			}
			http.Error(w, msg, http.StatusServiceUnavailable)
			return
		}
	}
}

// finished counts one forwarded request out.
func (f *Front) finished() {
	f.mu.Lock()
	f.inflight--
	f.broadcast()
	f.mu.Unlock()
}

// wakeLoop asks for the card until berthd queues the ask, the engine is
// open again, or the hold runs out. It does not belong to any one request:
// a client that hangs up does not cancel the wake the others wait on.
func (f *Front) wakeLoop() {
	ctx, cancel := context.WithTimeout(context.Background(), f.hold)
	defer cancel()
	for {
		err := f.wake(ctx)
		f.mu.Lock()
		if err == nil || f.state != waking {
			f.why = ""
			f.mu.Unlock()
			return
		}
		f.why = err.Error()
		f.broadcast()
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			f.mu.Lock()
			if f.state == waking {
				f.set(asleep) // the next request asks again
			}
			f.mu.Unlock()
			return
		case <-time.After(f.retry):
		}
	}
}

// Sleep stops forwarding and waits up to within for the requests in flight
// to finish. True means the engine may sleep and every new request is held;
// false means a request outlived the wait, and the front forwards again.
func (f *Front) Sleep(within time.Duration) bool {
	deadline := time.After(within)
	f.mu.Lock()
	f.set(closing)
	for f.inflight > 0 {
		ch := f.changed
		f.mu.Unlock()
		select {
		case <-ch:
		case <-deadline:
			f.mu.Lock()
			f.set(open)
			f.mu.Unlock()
			return false
		}
		f.mu.Lock()
	}
	f.set(asleep)
	f.mu.Unlock()
	return true
}

// Open forwards again: the engine is awake, or its sleep did not happen.
func (f *Front) Open() {
	f.mu.Lock()
	f.set(open)
	f.mu.Unlock()
}

// String names the state, for the run's log.
func (f *Front) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf("%s, %d in flight", [...]string{"open", "closing", "asleep", "waking"}[f.state], f.inflight)
}
