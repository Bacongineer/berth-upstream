// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Drain path (no idle session): the boundary is the queue alone. A dead
// /history must never fire the bound while the queue reports a running job.
func TestDrainPathNeverCutsJobOnDeadHistory(t *testing.T) {
	var busyUntil = time.Now().Add(2500 * time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			if time.Now().Before(busyUntil) {
				fmt.Fprint(w, `{"queue_running":[[1,2,[3],{},{}]],"queue_pending":[]}`)
			} else {
				fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			}
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) }
	result := make(chan bool, 1)
	go func() {
		// ExitComfyUIIdle=false: the plain drain boundary.
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, FailClosed: false,
			Engine: "comfyui", Retry: 20 * time.Millisecond, Grace: 50 * time.Millisecond}, done, say)
	}()
	select {
	case ok := <-result:
		// The queue reports the job until busyUntil, so the boundary may
		// fire at that instant and no earlier. A stopwatch started after
		// busyUntil was set measured a hair under it on a coarse clock.
		if early := time.Until(busyUntil); early > 0 {
			t.Fatalf("boundary fired %v before the queue emptied, while it reported a running job: %q",
				early.Round(time.Millisecond), logs.String())
		}
		if !ok {
			t.Fatalf("fail-open drain refused after the queue emptied: %q", logs.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("drain boundary never completed after the queue emptied: %q", logs.String())
	}
}

// Session path: an entry-time /history failure (ComfyUI booting) must not
// arm the bound against a readable busy queue.
func TestEntryHistoryBlipDoesNotBoundBusyQueue(t *testing.T) {
	var historyOK atomic.Bool // dead at entry, recovers after 300ms
	go func() { time.Sleep(300 * time.Millisecond); historyOK.Store(true) }()
	var busy = time.Now().Add(1200 * time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			if time.Now().Before(busy) {
				fmt.Fprint(w, `{"queue_running":[[1,2,[3],{},{}]],"queue_pending":[]}`)
			} else {
				fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			}
		case "/history":
			if historyOK.Load() {
				fmt.Fprint(w, `{"job1":{}}`)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	logs := &strings.Builder{}
	say := func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) }
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true,
			ExitComfyUIIdleAfter: 10 * time.Second, FailClosed: true, Engine: "comfyui",
			Retry: 20 * time.Millisecond, Grace: 50 * time.Millisecond}, done, say)
	}()
	// The bound is 200ms; the queue is busy for 1200ms. A refusal inside
	// the busy window is the defect.
	select {
	case <-result:
		if strings.Contains(logs.String(), "drain refused") {
			t.Fatalf("fail-closed refused a readable busy queue after an entry history blip: %q", logs.String())
		}
	case <-time.After(900 * time.Millisecond):
		// still waiting at 900ms with bound=200ms: correct for a busy queue
		close(done)
		select {
		case <-result:
		case <-time.After(3 * time.Second):
			t.Fatal("boundary never resolved after the queue emptied")
		}
		if strings.Contains(logs.String(), "drain refused") {
			t.Fatalf("fail-closed refused the busy session after the entry blip: %q", logs.String())
		}
	}
}

// A transient /history failure that recovers must disarm the history timer:
// the bound measures continuous failure, not time since the first blip.
func TestRecoveredHistoryDisarmsTheBound(t *testing.T) {
	var blip atomic.Bool
	var entry = time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			if blip.Load() {
				w.WriteHeader(http.StatusInternalServerError)
			} else if time.Now().Before(entry.Add(500 * time.Millisecond)) {
				fmt.Fprint(w, `{}`) // baseline at entry: no keys
			} else {
				fmt.Fprint(w, `{"job1":{}}`) // the session's job completed
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	// The blip starts after the baseline is captured and lasts 100ms;
	// bound = 4*Grace = 200ms, so the success at the end of the blip
	// must disarm the timer before the bound can trip.
	go func() {
		time.Sleep(200 * time.Millisecond)
		blip.Store(true)
		time.Sleep(100 * time.Millisecond)
		blip.Store(false)
	}()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) }
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true,
			ExitComfyUIIdleAfter: 10 * time.Second, FailClosed: true, Engine: "comfyui",
			Retry: 20 * time.Millisecond, Grace: 50 * time.Millisecond}, done, say)
	}()
	select {
	case ok := <-result:
		if !ok || strings.Contains(logs.String(), "drain refused") {
			t.Fatalf("recovered history did not disarm the bound: refusal during the blip: %q", logs.String())
		}
		if !strings.Contains(logs.String(), "cannot read ComfyUI history") {
			t.Fatalf("blip never happened in the log: %q", logs.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("boundary never resolved after the session job: %q", logs.String())
	}
}
