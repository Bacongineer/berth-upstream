// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/config"
)

// fallbackSrv is a fallback that is up at once: it answers the probe, records
// the model the proxied request carried and streams its own reply, so a test
// can tell whose answer arrived and under which name.
func fallbackSrv(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	model := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"cpu"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &req)
		mu.Lock()
		model = req.Model
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"from the cpu fallback\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() string { mu.Lock(); defer mu.Unlock(); return model }
}

// fallbackGateway pairs the primary (the fake child engine, which answers
// only after load) with a fallback at fbURL the request may take after wait.
// It returns the gateway's test server, the primary's base URL and the
// daemon, whose leases say who was really started.
func fallbackGateway(t *testing.T, load, wait time.Duration, fbModel, fbURL string, log io.Writer) (*httptest.Server, string, *broker.Broker) {
	t.Helper()
	sock, b := daemon(t)
	primary := freeAddr(t)
	t.Setenv("BERTH_GW_ENGINE", primary+",big,"+load.String())
	g := &Gateway{Socket: sock, Ready: 10 * time.Second, Key: testKey,
		Log: slog.New(slog.NewTextHandler(log, nil)),
		Engines: []config.Engine{
			{Name: "strata", Type: "llamacpp", URL: "http://" + primary, Start: []string{os.Args[0]},
				Serves: []string{"big"}, Priority: "interactive",
				Fallback: "strata-cpu", FallbackAfter: config.Duration{Duration: wait}, FallbackModel: fbModel},
			{Name: "strata-cpu", Type: "llamacpp", URL: fbURL, Serves: []string{"cpu"}},
		}}
	gw := httptest.NewServer(g.Handler())
	t.Cleanup(gw.Close)
	return gw, "http://" + primary, b
}

// A request whose engine is slow to come up goes to a fallback that answers
// now: the answer carries X-Berth-Fallback, the model arrives under the name
// the fallback lists, the switch is journalled once, and the primary's own
// start is not cancelled by it.
func TestFallbackAnswersWhenThePrimaryIsSlow(t *testing.T) {
	fb, saw := fallbackSrv(t)
	log := &syncBuf{}
	gw, primary, b := fallbackGateway(t, 3*time.Second, 300*time.Millisecond, "cpu", fb.URL, log)

	start := time.Now()
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"big"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Berth-Fallback"); got != "strata-cpu" {
		t.Fatalf("X-Berth-Fallback %q, want strata-cpu", got)
	}
	if !strings.Contains(string(body), "from the cpu fallback") {
		t.Fatalf("the fallback did not answer: %s", body)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the request waited %s; it should have gone to the fallback", elapsed.Round(time.Millisecond))
	}
	if got := saw(); got != "cpu" {
		t.Fatalf("the fallback saw model %q, want the rewritten \"cpu\"", got)
	}
	s := log.String()
	if !strings.Contains(s, "gateway request fell back") || !strings.Contains(s, "engine=strata") ||
		!strings.Contains(s, "fallback=strata-cpu") || !strings.Contains(s, "waited=") {
		t.Fatalf("the switch is not journalled: %s", s)
	}
	if n := strings.Count(s, "gateway request fell back"); n != 1 {
		t.Fatalf("%d fallback lines for one request: %s", n, s)
	}

	// The gateway never leases the fallback, and the primary's start went on
	// answering for the requests that come after this one.
	for _, l := range b.Status().Leases {
		if l.Engine == "strata-cpu" {
			t.Fatal("the gateway leased the fallback")
		}
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		up, err := http.Get(primary + "/v1/models")
		if err == nil {
			ok := up.StatusCode == http.StatusOK
			up.Body.Close()
			if ok {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the primary's start was cancelled by the fallback: it never answered")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Without fallback_model the fallback sees the request's own model name.
func TestFallbackWithoutAModelKeepsTheRequest(t *testing.T) {
	fb, saw := fallbackSrv(t)
	gw, _, _ := fallbackGateway(t, 3*time.Second, 300*time.Millisecond, "", fb.URL, &syncBuf{})
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"big"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Berth-Fallback") != "strata-cpu" ||
		!strings.Contains(string(body), "from the cpu fallback") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := saw(); got != "big" {
		t.Fatalf("the fallback saw model %q, want the request's own \"big\"", got)
	}
}

// The primary answering before fallback_after keeps the request: no header
// marks it, and the fallback is never asked.
func TestNoFallbackWhenThePrimaryAnswersFirst(t *testing.T) {
	fb, saw := fallbackSrv(t)
	start := time.Now()
	gw, _, _ := fallbackGateway(t, 300*time.Millisecond, 5*time.Second, "", fb.URL, &syncBuf{})
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"big"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "never collide") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Berth-Fallback") != "" {
		t.Fatal("the primary's answer is marked as a fallback")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the request waited %s, longer than the engine's load", elapsed.Round(time.Millisecond))
	}
	if got := saw(); got != "" {
		t.Fatalf("the fallback was asked: model %q", got)
	}
}

// A fallback that is not answering means no fallback: the request keeps
// waiting for its engine and completes on it.
func TestNoFallbackWhenTheFallbackIsDown(t *testing.T) {
	log := &syncBuf{}
	gw, _, _ := fallbackGateway(t, time.Second, 200*time.Millisecond, "", "http://"+freeAddr(t), log)
	start := time.Now()
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"big"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "never collide") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Berth-Fallback") != "" {
		t.Fatal("the primary's answer is marked as a fallback")
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("the request answered after %s, before the engine was up", elapsed.Round(time.Millisecond))
	}
	if strings.Contains(log.String(), "gateway request fell back") {
		t.Fatalf("a down fallback was journalled as a switch: %s", log.String())
	}
}

// A streaming request whose keep-alive preamble is already on the wire gets
// the fallback's stream through the same body-only path the primary would
// use: the loading comments stop, one comment marks the switch, and the
// fallback's own data follows.
func TestFallbackStreamAfterThePreamble(t *testing.T) {
	shortKeepAlive(t, 100*time.Millisecond)
	fb, saw := fallbackSrv(t)
	gw, _, _ := fallbackGateway(t, 3*time.Second, 400*time.Millisecond, "cpu", fb.URL, &syncBuf{})
	resp := call(t, "POST", gw.URL+"/v1/chat/completions",
		`{"model":"big","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type %q, want text/event-stream", ct)
	}
	if resp.Header.Get("X-Berth-Fallback") != "" {
		t.Fatal("the headers went out with the preamble; a fallback header cannot follow")
	}
	var loading, data int
	switched := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ": berth: loading"):
			loading++
		case strings.HasPrefix(line, ": berth: fallback"):
			if line != ": berth: fallback strata-cpu" {
				t.Fatalf("fallback comment = %q", line)
			}
			switched = true
		case strings.HasPrefix(line, "data: "):
			data++
		}
	}
	if loading == 0 {
		t.Error("no loading comment arrived before the switch")
	}
	if !switched {
		t.Error("no fallback comment arrived")
	}
	if data != 2 {
		t.Errorf("data lines = %d, want the fallback's reply and DONE", data)
	}
	if got := saw(); got != "cpu" {
		t.Errorf("the fallback saw model %q, want the rewritten \"cpu\"", got)
	}
}

// An engine that names a fallback but sets no time waits 10 s.
func TestFallbackAfterDefaults(t *testing.T) {
	g := &Gateway{}
	for _, c := range []struct {
		e    config.Engine
		want time.Duration
	}{
		{config.Engine{Fallback: "cpu"}, 10 * time.Second},
		{config.Engine{Fallback: "cpu", FallbackAfter: config.Duration{Duration: 30 * time.Second}}, 30 * time.Second},
	} {
		if got := g.fallbackAfter(&c.e); got != c.want {
			t.Errorf("fallbackAfter(%+v) = %s, want %s", c.e, got, c.want)
		}
	}
}
