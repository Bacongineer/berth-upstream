// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// comfyStub is a ComfyUI stand-in: an empty or busy queue, a /free that
// records what it was asked, and stats that free the device unless hold is
// set (an unload that did not take, for the timeout fallback).
type comfyStub struct {
	busy   atomic.Bool
	hold   atomic.Bool
	freed  atomic.Bool
	mu     sync.Mutex
	frees  int
	bodies []string
}

func (c *comfyStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		if c.busy.Load() {
			fmt.Fprint(w, `{"queue_running":[[1,"job"]],"queue_pending":[]}`)
			return
		}
		fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
	})
	mux.HandleFunc("/free", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.frees++
		c.bodies = append(c.bodies, string(b))
		c.mu.Unlock()
		if !c.hold.Load() {
			c.freed.Store(true)
		}
		fmt.Fprint(w, `{}`)
	})
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, r *http.Request) {
		used := uint64(17) << 30
		if c.freed.Load() {
			used = 512 << 20
		}
		total := uint64(24) << 30
		fmt.Fprintf(w, `{"devices":[{"vram_free":%d,"vram_total":%d}]}`, total-used, total)
	})
	return mux
}

// startSleep starts a stand-in engine and reaps it; waited fires with its
// exit when (and only when) the process actually dies.
func startSleep(t *testing.T) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	isolate(cmd) // as command() does: the drain signals the process group
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	// Cleanup only kills: the reaper above always collects the exit into
	// the buffered channel, so nothing here may receive from waited again.
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, waited
}

func sayTo(logs *strings.Builder) func(string, ...any) {
	return func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) }
}

// Without keep-up the server is stopped after the unload: the process dies.
func TestDrainUnloadsThenStops(t *testing.T) {
	stub := &comfyStub{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	drain(Options{Engine: "comfyui", DrainComfyUI: srv.URL,
		Grace: 50 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatalf("server still alive after the drain; log:\n%s", &logs)
	}
	stub.mu.Lock()
	frees := stub.frees
	stub.mu.Unlock()
	if frees != 1 {
		t.Fatalf("/free hits = %d, want 1 (unload-first); log:\n%s", frees, &logs)
	}
}

// A failed unload falls back to stopping the server: the process dies and
// the lease is not released early.
func TestDrainUnloadFailureStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		default:
			http.Error(w, "wedged", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	drain(Options{Engine: "comfyui", DrainComfyUI: srv.URL,
		Grace: 50 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatalf("server still alive after failed unload; log:\n%s", &logs)
	}
	if out := logs.String(); !strings.Contains(out, "stopping the server instead") {
		t.Fatalf("no fallback line; log:\n%s", out)
	}
}

// slotStub is a drain-post target with a llama.cpp-style /slots and an
// unload URL that records its hits.
type slotStub struct {
	busy     atomic.Bool
	mu       sync.Mutex
	unloads  int
	busySeen int
}

func (s *slotStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/slots", func(w http.ResponseWriter, r *http.Request) {
		if s.busy.Load() {
			s.mu.Lock()
			s.busySeen++
			s.mu.Unlock()
			fmt.Fprint(w, `[{"id":0,"is_processing":true}]`)
			return
		}
		fmt.Fprint(w, `[{"id":0,"is_processing":false}]`)
	})
	mux.HandleFunc("/unload", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.unloads++
		s.mu.Unlock()
		fmt.Fprint(w, `{}`)
	})
	return mux
}

func (s *slotStub) counts() (unloads, busySeen int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unloads, s.busySeen
}

// The unload waits past a busy boundary: nothing is posted while slots
// process, and it fires once they idle.
func TestDrainSlotBoundaryWaits(t *testing.T) {
	stub := &slotStub{}
	stub.busy.Store(true)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		drain(Options{Engine: "llama-swap", DrainPost: srv.URL + "/unload",
			Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))
	}()

	time.Sleep(200 * time.Millisecond)
	if unloads, seen := stub.counts(); unloads != 0 {
		t.Fatalf("unloaded while slots were busy")
	} else if seen == 0 {
		t.Fatal("drain never read the busy boundary")
	}

	stub.busy.Store(false)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after slots idled")
	}
	if unloads, _ := stub.counts(); unloads != 1 {
		t.Fatalf("unloads = %d, want 1", unloads)
	}
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine still alive after the drain")
	}
	if out := logs.String(); !strings.Contains(out, "engine idle; unloading") {
		t.Fatalf("no boundary line; log:\n%s", out)
	}
}

// --drain-slots, for an engine with no unload URL (Strata): the engine keeps
// running while a slot is processing, is stopped once the slots idle, and
// nothing is posted to it.
func TestDrainSlotsWaitsThenStops(t *testing.T) {
	stub := &slotStub{}
	stub.busy.Store(true)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder
	go drain(Options{Engine: "strata", DrainSlots: srv.URL,
		Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))

	time.Sleep(200 * time.Millisecond)
	select {
	case <-waited:
		t.Fatal("engine stopped while a slot was processing")
	default:
	}
	if _, seen := stub.counts(); seen == 0 {
		t.Fatal("drain never read the busy boundary")
	}

	stub.busy.Store(false)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("engine still alive after its slots idled")
	}
	if unloads, _ := stub.counts(); unloads != 0 {
		t.Fatalf("drain-slots posted %d unloads, want none", unloads)
	}
}

// A drain-post host with no /slots unloads at once instead of hanging.
func TestDrainNoSlotsProceeds(t *testing.T) {
	var mu sync.Mutex
	unloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/unload" {
			mu.Lock()
			unloads++
			mu.Unlock()
			fmt.Fprint(w, `{}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cmd, _ := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	start := time.Now()
	drain(Options{Engine: "llama-swap", DrainPost: srv.URL + "/unload",
		Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("drain took %s without a slot boundary", el)
	}
	mu.Lock()
	defer mu.Unlock()
	if unloads != 1 {
		t.Fatalf("unloads = %d, want 1; log:\n%s", unloads, &logs)
	}
	if out := logs.String(); !strings.Contains(out, "no slot boundary") {
		t.Fatalf("no fail-open line; log:\n%s", out)
	}
}

// A sick boundary waits instead of cutting work off: 500s from /slots
// hold the unload back, and it fires once the slots read idle.
func TestDrainSlotSickWaits(t *testing.T) {
	var mu sync.Mutex
	sick := true
	unloads, polls500 := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/unload":
			unloads++
			fmt.Fprint(w, `{}`)
		case "/slots":
			if sick {
				polls500++
				http.Error(w, "wedged", http.StatusInternalServerError)
				return
			}
			fmt.Fprint(w, `[{"id":0,"is_processing":false}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		drain(Options{Engine: "llama-swap", DrainPost: srv.URL + "/unload",
			Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))
	}()

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	u, p := unloads, polls500
	mu.Unlock()
	if u != 0 {
		t.Fatal("unloaded while the slot boundary was sick")
	}
	if p == 0 {
		t.Fatal("drain never polled the sick boundary")
	}

	mu.Lock()
	sick = false
	mu.Unlock()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after slots read idle")
	}
	mu.Lock()
	u = unloads
	mu.Unlock()
	if u != 1 {
		t.Fatalf("unloads = %d, want 1; log:\n%s", u, &logs)
	}
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine still alive after the drain")
	}
	if out := logs.String(); !strings.Contains(out, "engine idle; unloading") {
		t.Fatalf("no boundary line; log:\n%s", out)
	}
}

// Scenario 6 from contrib/chaos, fixed: an engine that keeps reporting a
// processing slot ends its drain after drain_max, and the drain runs the
// normal stop ladder instead of holding the card until someone notices.
func TestDrainSlotBusyMaxStops(t *testing.T) {
	stub := &slotStub{}
	stub.busy.Store(true)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		drain(Options{Engine: "strata", DrainSlots: srv.URL, DrainMax: 300 * time.Millisecond,
			Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not end after drain_max")
	}
	if out := logs.String(); !strings.Contains(out, "still busy after 300ms (1 slot processing); draining anyway") {
		t.Fatalf("no ceiling line; log:\n%s", out)
	}
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine survived the stop ladder after drain_max")
	}
	if unloads, _ := stub.counts(); unloads != 0 {
		t.Fatalf("drain-slots posted %d unloads, want none", unloads)
	}
}

// With no drain_max the wait stays unbounded: a busy boundary holds the
// stop ladder back for as long as it reads busy (a long generation is
// never cut off), and the journal says why. The wait ends when the engine
// exits, which Run marks by closing done.
func TestDrainSlotBusyUnboundedWaits(t *testing.T) {
	stub := &slotStub{}
	stub.busy.Store(true)
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	orig := drainProgressEvery
	drainProgressEvery = 50 * time.Millisecond
	t.Cleanup(func() { drainProgressEvery = orig })

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		drain(Options{Engine: "strata", DrainSlots: srv.URL,
			Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond}, cmd, newLine(nil), done, "test", sayTo(&logs))
	}()

	// After 500 ms the engine must still be running: with no max the
	// busy boundary holds the stop ladder back, and the drain has kept
	// reading it.
	time.Sleep(500 * time.Millisecond)
	select {
	case <-waited:
		t.Fatal("engine stopped while its boundary stayed busy and no max was set")
	default:
	}
	if _, seen := stub.counts(); seen == 0 {
		t.Fatal("drain never read the busy boundary")
	}

	// The operator's way out: stop the engine, as Run would on its exit.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not exit")
	}
	close(done)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("drain did not end when the engine exited")
	}
	// Read the log only once the drain goroutine has ended.
	if out := logs.String(); !strings.Contains(out, "1 slot busy for") {
		t.Fatalf("no progress line while the wait ran; log:\n%s", out)
	}
}

// failOpen tells "no boundary here" from "boundary sick": 404, garbage,
// and an unreachable host fail open; 500 and timeouts wait.
func TestSlotsFailOpenKinds(t *testing.T) {
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "wedged", http.StatusInternalServerError)
	}))
	defer sick.Close()
	if _, err := slotsBusy(sick.URL); err == nil || failOpen(err) {
		t.Fatalf("500 should wait, got %v", err)
	}

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer missing.Close()
	if _, err := slotsBusy(missing.URL); err == nil || !failOpen(err) {
		t.Fatalf("404 should fail open, got %v", err)
	}

	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not json`)
	}))
	defer garbage.Close()
	if _, err := slotsBusy(garbage.URL); err == nil || !failOpen(err) {
		t.Fatalf("bad JSON should fail open, got %v", err)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if _, err := slotsBusy(deadURL); err == nil || !failOpen(err) {
		t.Fatalf("unreachable should fail open, got %v", err)
	}

	if !slotsTimeout(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded is a timeout")
	}
	if slotsTimeout(errors.New("connection refused")) {
		t.Fatal("refused is not a timeout")
	}
	if failOpen(&slotsErr{timeout: true, err: context.DeadlineExceeded}) {
		t.Fatal("a timeout should wait, not fail open")
	}
}
