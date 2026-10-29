// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
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
	rep.Alert("crit", "temp", "gpu at 95c", "")
	if dt := time.Since(start); dt > 200*time.Millisecond {
		t.Fatalf("Alert blocked the caller for %v; the sampler cycle would stall", dt)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook never hit")
	}
}

// The webhook payloads name the card when it is known, and leave the field
// out when it is not.
func TestWebhookPayloadsNameTheCard(t *testing.T) {
	got := make(chan map[string]any, 3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got <- m
	}))
	defer srv.Close()
	rep := Reporter{Webhook: srv.URL, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	next := func() map[string]any {
		select {
		case m := <-got:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("webhook never hit")
			return nil
		}
	}

	rep.Squatter(true, "gpu1", gpu.Proc{PID: 7, Name: "mpv", Used: 64 * gpu.MiB})
	if m := next(); m["event"] != "squatter.appeared" || m["card"] != "gpu1" || m["name"] != "mpv" {
		t.Fatalf("squatter payload = %v", m)
	}
	rep.Alert("warn", "temp", "temp 85C on gpu1", "gpu1")
	if m := next(); m["key"] != "temp" || m["card"] != "gpu1" {
		t.Fatalf("alert payload = %v", m)
	}
	rep.Alert("crit", "probe", "gpu probe failing", "")
	if m := next(); m["key"] != "probe" {
		t.Fatalf("alert payload = %v", m)
	} else if _, ok := m["card"]; ok {
		t.Fatalf("machine-wide alert carries a card: %v", m)
	}
}
