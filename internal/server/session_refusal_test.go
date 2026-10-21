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

// A pinned ComfyUI session: pinned + fail-closed + exit_comfyui_idle.
// The session boundary fires, the drain is refused by a sick slot boundary,
// and the lease is kept. The idle detector must re-arm: the next completed
// job of the session must release the card once the boundary recovers.
func TestIdleReleaseReArmsAfterRefusal(t *testing.T) {
	var okSlots atomic.Bool
	slots := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if okSlots.Load() {
			fmt.Fprint(w, `[]`)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer slots.Close()
	var job atomic.Int32
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			n := job.Load()
			if n == 0 {
				fmt.Fprint(w, `{}`)
			} else {
				fmt.Fprintf(w, `{"job%d":{}}`, n)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer comfy.Close()

	sock := start(t)

	// A: the resident ComfyUI session (pinned, fail-closed,
	// idle release, 200ms grace -> bound 800ms).
	a, outA := launch(runner.Options{
		Socket: sock, Engine: "comfyui", Priority: "foreground", Pinned: true,
		DrainComfyUI: comfy.URL, DrainSlots: slots.URL, ExitComfyUIIdle: true,
		FailClosed: true, Grace: 200 * time.Millisecond, Retry: 50 * time.Millisecond,
		Argv: []string{"sleep", "60"},
	})
	waitFor(t, "A grant", func() bool { return strings.Contains(outA.String(), "granted comfyui") })

	// The session's first completed job: the boundary fires while the slot
	// boundary is sick, so the drain refuses and A keeps its lease.
	job.Store(1)
	waitFor(t, "A idle refusal", func() bool {
		return strings.Contains(outA.String(), "drain refused")
	})

	// The same completed job must release the session after recovery;
	// another job is not required to retry a pending release.
	okSlots.Store(true)
	select {
	case <-a:
	case <-time.After(30 * time.Second):
		t.Fatalf("pinned lease wedged: idle detector fired once and never re-armed:\n%s", outA.String())
	}
	if n := strings.Count(outA.String(), "ComfyUI session complete"); n < 2 {
		t.Fatalf("idle detector fired %d time(s); expected at least 2", n)
	}
}
