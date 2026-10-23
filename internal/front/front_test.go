// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package front

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// engine is a stub engine: /slow blocks until release is closed, every
// other path answers its own path.
type engine struct {
	release chan struct{}
	started chan struct{}
}

func newEngine(t *testing.T) (*engine, *url.URL) {
	t.Helper()
	e := &engine{release: make(chan struct{}), started: make(chan struct{}, 4)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			e.started <- struct{}{}
			<-e.release
		}
		fmt.Fprint(w, "engine "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	return e, u
}

// fetch is one GET; goroutines use it, since only the test's own
// goroutine may call t.Fatal.
func fetch(base, path string) (int, string) {
	resp, err := http.Get(base + path)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// send is one POST, the kind of request that wakes a sleeping engine.
func send(base, path string) (int, string) {
	resp, err := http.Post(base+path, "application/json", strings.NewReader(`{}`))
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func get(t *testing.T, base, path string) (int, string) {
	t.Helper()
	code, body := fetch(base, path)
	if code == 0 {
		t.Fatal(body)
	}
	return code, body
}

func TestOpenForwards(t *testing.T) {
	_, target := newEngine(t)
	f := New(target, func(context.Context) error { t.Fatal("an open front never wakes"); return nil }, time.Second)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if code, body := get(t, srv.URL, "/v1/models"); code != 200 || body != "engine /v1/models" {
		t.Fatalf("got %d %q", code, body)
	}
}

// Sleep waits for the request in flight, and from then on new requests
// are held, not forwarded.
func TestSleepWaitsForInFlightThenHolds(t *testing.T) {
	e, target := newEngine(t)
	f := New(target, func(context.Context) error { return nil }, 200*time.Millisecond)
	srv := httptest.NewServer(f)
	defer srv.Close()

	slow := make(chan string, 1)
	go func() { _, body := fetch(srv.URL, "/slow"); slow <- body }()
	<-e.started

	slept := make(chan bool, 1)
	go func() { slept <- f.Sleep(5 * time.Second) }()
	select {
	case <-slept:
		t.Fatal("Sleep returned while a request was in flight")
	case <-time.After(100 * time.Millisecond):
	}
	close(e.release)
	if body := <-slow; body != "engine /slow" {
		t.Fatalf("in-flight request got %q", body)
	}
	if !<-slept {
		t.Fatal("Sleep = false after the in-flight request finished")
	}
	code, body := send(srv.URL, "/v1/chat/completions")
	if code != http.StatusServiceUnavailable || body != "the engine is asleep in RAM and Berth has not given it the card back yet\n" {
		t.Fatalf("held request got %d %q", code, body)
	}
}

// Two requests to a sleeping engine ask for the card once, and both are
// forwarded when the run opens the front.
func TestRequestsWakeOnceAndForwardAfterOpen(t *testing.T) {
	_, target := newEngine(t)
	var wakes atomic.Int32
	woke := make(chan struct{}, 1)
	f := New(target, func(context.Context) error {
		wakes.Add(1)
		woke <- struct{}{}
		return nil
	}, 5*time.Second)
	srv := httptest.NewServer(f)
	defer srv.Close()
	if !f.Sleep(time.Second) {
		t.Fatal("Sleep with nothing in flight = false")
	}

	var wg sync.WaitGroup
	bodies := make([]string, 2)
	for i := range bodies {
		wg.Add(1)
		go func() { defer wg.Done(); _, bodies[i] = send(srv.URL, "/v1/chat/completions") }()
	}
	<-woke
	time.Sleep(50 * time.Millisecond) // the second request is held too
	f.Open()
	wg.Wait()
	if wakes.Load() != 1 {
		t.Fatalf("wake asked %d times, want 1", wakes.Load())
	}
	for _, b := range bodies {
		if b != "engine /v1/chat/completions" {
			t.Fatalf("bodies = %q", bodies)
		}
	}
}

// A refused wake is asked again, and a held request that runs out names
// the refusal.
func TestRefusedWakeRetriesAndNamesTheReason(t *testing.T) {
	_, target := newEngine(t)
	var wakes atomic.Int32
	f := New(target, func(context.Context) error {
		wakes.Add(1)
		return errors.New("comfyui is not parked")
	}, 300*time.Millisecond)
	f.retry = 50 * time.Millisecond
	srv := httptest.NewServer(f)
	defer srv.Close()
	f.Sleep(time.Second)

	code, body := send(srv.URL, "/v1/chat/completions")
	if code != http.StatusServiceUnavailable ||
		body != "the engine is asleep in RAM and Berth has not given it the card back yet: comfyui is not parked\n" {
		t.Fatalf("got %d %q", code, body)
	}
	if n := wakes.Load(); n < 3 {
		t.Fatalf("wake asked %d times in 300 ms at a 50 ms retry", n)
	}
}

// A request that outlives the wait keeps the engine awake: Sleep says no
// and the front forwards again.
func TestSleepGivesUpOnALongRequest(t *testing.T) {
	e, target := newEngine(t)
	f := New(target, func(context.Context) error { return nil }, time.Second)
	srv := httptest.NewServer(f)
	defer srv.Close()
	go fetch(srv.URL, "/slow")
	<-e.started
	if f.Sleep(100 * time.Millisecond) {
		t.Fatal("Sleep = true with a request still in flight")
	}
	if code, body := get(t, srv.URL, "/health"); code != 200 || body != "engine /health" {
		t.Fatalf("after a refused sleep got %d %q", code, body)
	}
	close(e.release)
}

// A read never wakes a sleeping engine: a monitor's poll gets a 503 that
// says the engine is asleep, and no wake is asked for.
func TestReadsDoNotWake(t *testing.T) {
	_, target := newEngine(t)
	var wakes atomic.Int32
	f := New(target, func(context.Context) error { wakes.Add(1); return nil }, time.Second)
	srv := httptest.NewServer(f)
	defer srv.Close()
	f.Sleep(time.Second)
	for _, path := range []string{"/metrics", "/slots", "/health"} {
		if code, body := get(t, srv.URL, path); code != http.StatusServiceUnavailable || body != "the engine is asleep in RAM; a read does not wake it\n" {
			t.Fatalf("GET %s while asleep = %d %q", path, code, body)
		}
	}
	if n := wakes.Load(); n != 0 {
		t.Fatalf("reads asked for the card %d time(s)", n)
	}
	f.Open()
	if code, body := get(t, srv.URL, "/metrics"); code != 200 || body != "engine /metrics" {
		t.Fatalf("GET /metrics while open = %d %q", code, body)
	}
}
