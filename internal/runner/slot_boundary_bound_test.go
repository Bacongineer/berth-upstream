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

func TestFailClosedSickSlotBoundaryRefusesAfterGrace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(format string, a ...any) { fmt.Fprintf(logs, format+"\n", a...) }
	result := make(chan bool, 1)
	go func() {
		result <- slotBoundaryAt(Options{FailClosed: true, Retry: time.Millisecond, Grace: 20 * time.Millisecond, Engine: "strata"}, srv.URL, done, say)
	}()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("a sick boundary authorized a fail-closed unload")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a sick boundary wedged the drain forever")
	}
	if !strings.Contains(logs.String(), "drain refused") || !strings.Contains(logs.String(), "keeps its lease") {
		t.Fatalf("refused drain was not reported: %q", logs.String())
	}
}

func TestFailClosedMissingSlotBoundaryRefusesAtOnce(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	result := make(chan bool, 1)
	go func() {
		result <- slotBoundaryAt(Options{FailClosed: true, Retry: time.Millisecond, Grace: 20 * time.Millisecond, Engine: "strata"}, srv.URL, done, func(string, ...any) {})
	}()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("a slot-less engine authorized its own unload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a slot-less engine stalled the fail-closed drain")
	}
	if n := reads.Load(); n > 1 {
		t.Fatalf("a slot-less boundary was polled %d times", n)
	}
}

func TestFailOpenMissingSlotBoundaryStillUnloads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(format string, a ...any) { fmt.Fprintf(logs, format+"\n", a...) }
	if !slotBoundaryAt(Options{Retry: time.Millisecond, Grace: 20 * time.Millisecond, Engine: "llama"}, srv.URL, done, say) {
		t.Fatal("the fail-open contract changed: a missing boundary must unload")
	}
	if !strings.Contains(logs.String(), "unloading anyway") {
		t.Fatalf("fail-open notice missing: %q", logs.String())
	}
}
