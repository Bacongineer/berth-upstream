// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/config"
)

// keyEngine is an OpenAI-style engine that is up at once and records the
// Authorization of the chat request it receives, so a test can say what the
// gateway sent it.
func keyEngine(t *testing.T, model string) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	auth := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"object":"list","data":[{"id":%q}]}`, model)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"key-marker\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() string { mu.Lock(); defer mu.Unlock(); return auth }
}

// keyGateway pairs a configured engine at url with the test daemon and the
// gateway in front of it.
func keyGateway(t *testing.T, e config.Engine) (*httptest.Server, *broker.Broker) {
	t.Helper()
	sock, b := daemon(t)
	g := &Gateway{Socket: sock, Ready: 5 * time.Second, Key: testKey, Engines: []config.Engine{e}}
	gw := httptest.NewServer(g.Handler())
	t.Cleanup(gw.Close)
	return gw, b
}

// A client's node key authenticates it to the gateway and goes no further:
// an engine without its own credential receives no Authorization at all.
func TestProxySendsNoKeyToACredentiallessEngine(t *testing.T) {
	srv, saw := keyEngine(t, "llama3")
	gw, b := keyGateway(t, config.Engine{Name: "ollama", Type: "ollama", URL: srv.URL, Serves: []string{"llama3"}})
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"llama3"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "key-marker") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := saw(); got != "" {
		t.Fatalf("the engine received Authorization %q, want none", got)
	}
	if n := len(b.Status().Leases); n != 0 {
		t.Fatalf("%d leases for an engine that was already up", n)
	}
}
