// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/config"
)

// An agent (Hermes) chains work across three engines on one card through
// the gateway alone: its own model, a task handed to strata, strata given
// back once its part is done, a ComfyUI render at batch collected before
// the card moves on, then its own model again to review. Three drains,
// nothing refused, no juggle, one holder at the end.
func TestAgentWorkflowHandsTheCardAround(t *testing.T) {
	// berthd's engine readers report an engine idle once its reply is
	// done; that is what lets an equal-priority holder hand over inside the
	// juggle window instead of keeping the card.
	sock, b := daemonWith(t, broker.Config{Busy: func(string) (bool, bool) { return false, true }})
	hermes, strata, comfy := freeAddr(t), freeAddr(t), freeAddr(t)
	fake := func(env string) []string { return []string{"env", env, os.Args[0]} }
	g := &Gateway{Socket: sock, Ready: 10 * time.Second, Key: testKey, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engines: []config.Engine{
			{Name: "hermes", Type: "llamacpp", URL: "http://" + hermes, Serves: []string{"hermes-model"},
				Start: fake("BERTH_GW_ENGINE=" + hermes + ",hermes-model"), Priority: "interactive"},
			{Name: "strata", Type: "llamacpp", URL: "http://" + strata, Serves: []string{"strata-model"},
				Start: fake("BERTH_GW_ENGINE=" + strata + ",strata-model"), Priority: "interactive"},
			{Name: "comfyui", Type: "comfyui", URL: "http://" + comfy,
				Start: fake("BERTH_GW_COMFY=" + comfy), Priority: "batch"},
		}}
	gw := httptest.NewServer(g.Handler())
	defer gw.Close()

	send := func(step, method, path, body, want string) {
		t.Helper()
		resp := call(t, method, gw.URL+path, body)
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(got), want) {
			t.Fatalf("%s: %d %s", step, resp.StatusCode, got)
		}
	}
	chat := func(model string) string {
		return `{"model":"` + model + `","stream":true,"messages":[{"role":"user","content":"go"}]}`
	}
	holder := func() string {
		t.Helper()
		ls := b.Status().Leases
		if len(ls) != 1 {
			t.Fatalf("leases = %+v, want one holder", ls)
		}
		return ls[0].Engine
	}

	send("hermes plans", "POST", "/v1/chat/completions", chat("hermes-model"), "never collide")
	send("strata works", "POST", "/v1/chat/completions", chat("strata-model"), "never collide")
	if h := holder(); h != "strata" {
		t.Fatalf("after the handoff to strata the card is %s's", h)
	}
	// strata's part is done: the workflow gives the card back, or the batch
	// render below would wait behind an idle interactive holder.
	resp := call(t, "POST", gw.URL+"/v1/berth/stop", `{"engine":"strata"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("stop strata: %d", resp.StatusCode)
	}
	start := time.Now()
	send("comfyui renders", "POST", "/prompt", `{"prompt":{"3":{"class_type":"KSampler"}}}`, `"prompt_id":"gw-test-1"`)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("the render waited %s for a card strata had given back", waited)
	}
	send("render collected", "GET", "/history/gw-test-1", "", "history-marker")
	send("hermes reviews", "POST", "/v1/chat/completions", chat("hermes-model"), "never collide")
	if h := holder(); h != "hermes" {
		t.Fatalf("after the review the card is %s's", h)
	}

	c := b.Status().Counters
	if c.Drains != 3 || c.Refused != 0 || c.JugglesStopped != 0 {
		t.Fatalf("counters = %+v, want three drains, no refusal, no juggle", c)
	}
}
