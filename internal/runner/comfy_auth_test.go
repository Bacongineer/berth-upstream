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

func TestComfySessionUsesCredential(t *testing.T) {
	var completed atomic.Bool
	authed := atomic.Int32{}
	unauthed := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer fixture-comfy-key" {
			authed.Add(1)
		} else {
			unauthed.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-comfy-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			if completed.Load() {
				fmt.Fprint(w, `{"completed-fixture":{}}`)
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
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, APIKey: "fixture-comfy-key", ExitComfyUIIdle: true, Retry: time.Millisecond}, done, func(string, ...any) {})
	}()
	select {
	case <-result:
		t.Fatal("unused ComfyUI session exited before its first job")
	case <-time.After(50 * time.Millisecond):
	}
	if authed.Load() == 0 {
		t.Fatal("ComfyUI boundary omitted credential")
	}
	if unauthed.Load() != 0 {
		t.Fatalf("ComfyUI boundary made %d unauthenticated reads", unauthed.Load())
	}
	completed.Store(true)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("authenticated session boundary rejected a completed job")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated session did not release")
	}
}

func TestComfySessionIgnoresPreexistingHistory(t *testing.T) {
	var next atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			if next.Load() == 0 {
				fmt.Fprint(w, `{"previous-server-job":{}}`)
			} else {
				fmt.Fprint(w, `{"this-session-job":{}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true, Retry: time.Millisecond}, done, func(string, ...any) {})
	}()
	select {
	case <-result:
		t.Fatal("a fresh session self-drained on the server's lifetime history")
	case <-time.After(50 * time.Millisecond):
	}
	next.Store(1)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("the session's own completed job was rejected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the session never saw its own job")
	}
}

func TestComfyIdleSessionReleasesAfterDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(format string, a ...any) { fmt.Fprintf(logs, format+"\n", a...) }
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true, ExitComfyUIIdleAfter: 20 * time.Millisecond, Retry: time.Millisecond}, done, say)
	}()
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("idle deadline ended as a failed boundary")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an unused ComfyUI session never released the card")
	}
	if !strings.Contains(logs.String(), "no ComfyUI job within") {
		t.Fatalf("idle release was not reported: %q", logs.String())
	}
}
