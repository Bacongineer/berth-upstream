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

// The session baseline must be the history as it was when the
// boundary started. A job that completes before the first readable idle
// read must still count as this session's first completed job.
func TestSessionBaselineIsCapturedAtEntry(t *testing.T) {
	var jobDone atomic.Bool
	var busy atomic.Bool
	busy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			if busy.Load() {
				fmt.Fprint(w, `{"queue_running":[[1,2,[3],{},{}]],"queue_pending":[]}`)
			} else {
				fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			}
		case "/history":
			if jobDone.Load() {
				fmt.Fprint(w, `{"job1":{}}`)
			} else {
				fmt.Fprint(w, `{}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) }
	// The job completes while the boundary's first queue read is busy,
	// after the entry baseline has been captured.
	go func() {
		time.Sleep(80 * time.Millisecond)
		jobDone.Store(true)
		busy.Store(false)
	}()
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true,
			ExitComfyUIIdleAfter: 10 * time.Second, FailClosed: true, Engine: "comfyui",
			Retry: 20 * time.Millisecond, Grace: 30 * time.Second}, done, say)
	}()
	select {
	case ok := <-result:
		if !ok {
			t.Fatalf("session with a completed job refused instead of releasing: %q", logs.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("one-job session wedged: baseline captured after the job completed: %q", logs.String())
	}
}

// After the baseline is captured, the history read is still the
// session boundary. Its continuous failure must stamp the unreadable
// timer and trip the bound — swallowing the read error unbounded the lease.
func TestContinuousHistoryFailureAfterBaselineTripsTheBound(t *testing.T) {
	var historyOK atomic.Bool // baseline read succeeds once, then history dies
	var first atomic.Bool
	historyOK.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			if first.CompareAndSwap(false, true) {
				// seen busy once: a job was observed, idle deadline is off
				fmt.Fprint(w, `{"queue_running":[[1,2,[3],{},{}]],"queue_pending":[]}`)
			} else {
				fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			}
		case "/history":
			if historyOK.Load() {
				historyOK.Store(false) // the entry/baseline read succeeds
				fmt.Fprint(w, `{}`)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
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
		if ok {
			t.Fatalf("fail-closed released with an unreadable history after baseline: %q", logs.String())
		}
		if !strings.Contains(logs.String(), "drain refused") {
			t.Fatalf("refusal not reported: %q", logs.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("continuous /history failure after baseline never tripped the bound: %q", logs.String())
	}
}
