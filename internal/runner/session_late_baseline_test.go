// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An entry /history blip leaves the baseline nil; the session's
// job completes during the observed busy phase; the late re-baseline bakes
// that job into the baseline. The session's first completed job must
// release the boundary — not a second job.
func TestLateBaselineDoesNotEatSessionJob(t *testing.T) {
	var entry = time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			if time.Now().Before(entry.Add(600 * time.Millisecond)) {
				fmt.Fprint(w, `{"queue_running":[[1,2,[3],{},{}]],"queue_pending":[]}`)
			} else {
				fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			}
		case "/history":
			if time.Now().Before(entry.Add(300 * time.Millisecond)) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"job1":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	done := make(chan struct{})
	defer close(done)
	logs := &strings.Builder{}
	say := func(f string, a ...any) { fmt.Fprintf(logs, f+"\\n", a...) }
	result := make(chan bool, 1)
	go func() {
		result <- jobBoundary(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true,
			ExitComfyUIIdleAfter: 10 * time.Second, FailClosed: true, Engine: "comfyui",
			Retry: 20 * time.Millisecond, Grace: 50 * time.Millisecond}, done, say)
	}()
	select {
	case ok := <-result:
		if !ok {
			t.Fatalf("boundary refused the session after its job completed: %q", logs.String())
		}
	case <-time.After(4 * time.Second):
		t.Fatalf("boundary never released after the session job: %q", logs.String())
	}
}
