// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/park"
)

func TestCompletedSessionEndsInsteadOfParking(t *testing.T) {
	card := &parkCard{total: 24 << 30}
	sock, broker := parkDaemon(t, card, 24<<30)
	var completed atomic.Bool
	var parked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/queue":
			fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
		case "/history":
			if completed.Load() {
				fmt.Fprint(w, `{"fixture-job":{}}`)
			} else {
				fmt.Fprint(w, `{}`)
			}
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	var logs syncBuf
	exited := make(chan int, 1)
	go func() {
		exited <- Run(Options{Socket: sock, Engine: "fixture", Wait: time.Second, Grace: 20 * time.Millisecond, Retry: 5 * time.Millisecond, DrainComfyUI: srv.URL, ExitComfyUIIdle: true, Stderr: &logs, Argv: []string{"sleep", "30"}, Park: &park.Adapter{Park: func(context.Context, string) error { parked.Add(1); return nil }}})
	}()
	pid := attachAndMeasure(t, broker, card)
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Error("runner did not exit during cleanup")
		}
	})
	waitFor(t, "session baseline", func() bool { return strings.Contains(logs.String(), "waiting for the first ComfyUI job") })
	completed.Store(true)
	select {
	case code := <-exited:
		if code != 143 {
			t.Fatalf("exit = %d: %s", code, logs.String())
		}
		if parked.Load() != 0 {
			t.Fatal("completed session parked instead of ending")
		}
		exited <- code
	case <-time.After(3 * time.Second):
		t.Fatalf("completed session stayed up: %s", logs.String())
	}
}
