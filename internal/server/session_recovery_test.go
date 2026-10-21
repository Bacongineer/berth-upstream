// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/runner"
)

// The session's /history stays unreadable past the bound, so the
// boundary fires as a fail-closed refusal. That fire must not end the
// session detector: it keeps the lease, re-arms, and the session's first
// completed job releases the card once the boundary recovers.
func TestSessionBoundaryReArmsAfterBoundFire(t *testing.T) {
	var okHistory atomic.Bool
	var job atomic.Int32
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			if !okHistory.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if n := job.Load(); n == 0 {
				fmt.Fprint(w, `{}`)
			} else {
				fmt.Fprintf(w, `{"job%d":{}}`, n)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer comfy.Close()
	slots := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `[]`)
	}))
	defer slots.Close()

	sock := start(t)

	// Pinned, fail-closed, idle release; 200ms grace -> 800ms bound,
	// 30s idle deadline so the unreadable bound fires first.
	a, outA := launch(runner.Options{
		Socket: sock, Engine: "comfyui", Priority: "foreground", Pinned: true,
		DrainComfyUI: comfy.URL, DrainSlots: slots.URL, ExitComfyUIIdle: true,
		ExitComfyUIIdleAfter: 30 * time.Second, FailClosed: true,
		Grace: 200 * time.Millisecond, Retry: 50 * time.Millisecond,
		Argv: []string{"sleep", "60"},
	})
	waitFor(t, "A grant", func() bool { return strings.Contains(outA.String(), "granted comfyui") })

	// The unreadable history trips the bound: a refusal, lease kept.
	waitFor(t, "A bound fire", func() bool {
		return strings.Contains(outA.String(), "drain refused")
	})

	// The boundary recovers: the detector re-arms and re-baselines with
	// job1 baked in. The session's own job is job2 — the detector must
	// release A on its completion.
	okHistory.Store(true)
	job.Store(1)
	waitFor(t, "A re-baseline", func() bool {
		return strings.Contains(outA.String(), "waiting for the first ComfyUI job")
	})
	job.Store(2)
	select {
	case <-a:
	case <-time.After(30 * time.Second):
		t.Fatalf("session detector died on the bound fire; lease wedged:\n%s", outA.String())
	}
}
