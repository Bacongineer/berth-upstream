// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfySessionWaitsForFirstCompletedWork(t *testing.T) {
	var completed atomic.Bool
	requests := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			select {
			case requests <- struct{}{}:
			default:
			}
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
	o := Options{DrainComfyUI: srv.URL, Retry: time.Millisecond}
	if err := json.Unmarshal([]byte(`{"ExitComfyUIIdle":true}`), &o); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	result := make(chan bool, 1)
	go func() { result <- jobBoundary(o, done, func(string, ...any) {}) }()
	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("session queue was not read")
	}
	select {
	case <-result:
		t.Fatal("unused ComfyUI session exited before its first job")
	case <-time.After(20 * time.Millisecond):
	}
	completed.Store(true)
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("completed short job was not accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("completed session did not release")
	}
}
