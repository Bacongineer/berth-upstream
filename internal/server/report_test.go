// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestAlertDoesNotBlockCaller is the M1 proof: a webhook that never
// answers must not hold up the sampler goroutine. Alert returns
// immediately, and the webhook is still hit in the background.
func TestAlertDoesNotBlockCaller(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	rep := Reporter{Webhook: srv.URL, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	start := time.Now()
	rep.Alert("crit", "temp", "gpu at 95c")
	if dt := time.Since(start); dt > 200*time.Millisecond {
		t.Fatalf("Alert blocked the caller for %v; the sampler cycle would stall", dt)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook never hit")
	}
}
