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

func TestSessionIdleDeadlineSurvivesRefusal(t *testing.T) {
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/free":
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer comfy.Close()

	finished, output := launch(runner.Options{
		Socket: start(t), Engine: "idle-deadline", Priority: "foreground", Pinned: true,
		DrainComfyUI: comfy.URL, ExitComfyUIIdle: true,
		ExitComfyUIIdleAfter: 500 * time.Millisecond, FailClosed: true,
		Grace: 25 * time.Millisecond, Retry: 10 * time.Millisecond,
		Argv: []string{"sleep", "2"},
	})
	select {
	case code := <-finished:
		text := output.String()
		if !strings.Contains(text, "drain refused") {
			t.Fatalf("failure bound was not exercised:\n%s", text)
		}
		if !strings.Contains(text, "no ComfyUI job within") || code == 0 {
			t.Fatalf("refusal reset the idle deadline; session exited naturally instead of releasing:\n%s", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not exit within the child's natural lifetime")
	}
}

func TestSessionObservedWorkSurvivesRefusal(t *testing.T) {
	var phase atomic.Int32
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			if phase.Load() == 0 {
				fmt.Fprint(w, `{"queue_running":[[1,"job1"]],"queue_pending":[]}`)
			} else {
				fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
			}
		case "/history":
			switch phase.Load() {
			case 0:
				fmt.Fprint(w, `{}`)
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
			default:
				fmt.Fprint(w, `{"job1":{}}`)
			}
		case "/free":
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer comfy.Close()

	finished, output := launch(runner.Options{
		Socket: start(t), Engine: "observed-work", Priority: "foreground", Pinned: true,
		DrainComfyUI: comfy.URL, ExitComfyUIIdle: true,
		ExitComfyUIIdleAfter: 10 * time.Second, FailClosed: true,
		Grace: 25 * time.Millisecond, Retry: 10 * time.Millisecond,
		Argv: []string{"sleep", "2"},
	})
	waitFor(t, "observed running job", func() bool { return strings.Contains(output.String(), "waiting for ComfyUI to finish") })
	phase.Store(1)
	waitFor(t, "history boundary refusal", func() bool { return strings.Contains(output.String(), "drain refused") })
	phase.Store(2)
	select {
	case code := <-finished:
		if !strings.Contains(output.String(), "ComfyUI session complete") || code == 0 {
			t.Fatalf("refusal lost the observed job and required a second completion:\n%s", output.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not exit within the child's natural lifetime")
	}
}
