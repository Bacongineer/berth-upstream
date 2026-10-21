// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
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

// An engine with its own credential receives that key on every proxied
// request, not the node's.
func TestProxySendsTheEngineOwnKey(t *testing.T) {
	const env = "GW_TEST_ENGINE_KEY"
	t.Setenv(env, "engine-key-9876543210")
	srv, saw := keyEngine(t, "llama3")
	gw, _ := keyGateway(t, config.Engine{Name: "ollama", Type: "ollama", URL: srv.URL,
		Serves: []string{"llama3"}, APIKeyEnv: env})
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"llama3"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "key-marker") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := saw(); got != "Bearer engine-key-9876543210" {
		t.Fatalf("the engine received %q, want its own bearer", got)
	}
}

// A fallback that switched in for the slow primary carries the fallback's
// own key when it has one, instead of the plain drop the node key used to
// get.
func TestFallbackSendsItsOwnKey(t *testing.T) {
	const env = "GW_TEST_FALLBACK_KEY"
	t.Setenv(env, "fallback-key-123456")
	var mu sync.Mutex
	auth := ""
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"cpu"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"from the cpu fallback\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	fb := httptest.NewServer(mux)
	t.Cleanup(fb.Close)

	sock, b := daemon(t)
	primary := freeAddr(t)
	t.Setenv("BERTH_GW_ENGINE", primary+",big,3s")
	g := &Gateway{Socket: sock, Ready: 10 * time.Second, Key: testKey,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engines: []config.Engine{
			{Name: "strata", Type: "llamacpp", URL: "http://" + primary, Start: []string{os.Args[0]},
				Serves: []string{"big"}, Priority: "interactive",
				Fallback: "strata-cpu", FallbackAfter: config.Duration{Duration: 300 * time.Millisecond}, FallbackModel: "cpu"},
			{Name: "strata-cpu", Type: "llamacpp", URL: fb.URL, Serves: []string{"cpu"}, APIKeyEnv: env},
		}}
	gw := httptest.NewServer(g.Handler())
	t.Cleanup(gw.Close)

	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"big"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Berth-Fallback") != "strata-cpu" {
		t.Fatalf("status %d %q: the request did not go to the fallback", resp.StatusCode, body)
	}
	mu.Lock()
	got := auth
	mu.Unlock()
	if got != "Bearer fallback-key-123456" {
		t.Fatalf("the fallback received %q, want its own bearer", got)
	}
	if n := len(b.Status().Leases); n == 0 {
		t.Fatal("the primary's start never took its lease")
	}
}

// A ComfyUI route the gateway proxies (/prompt with the engine up, the
// side routes) reaches the engine with no Authorization: the node key is
// the client's credential for the gateway, and a ComfyUI with no key of
// its own wants none.
func TestComfyRoutesSendNoKey(t *testing.T) {
	var mu sync.Mutex
	auth := ""
	see := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if a := r.Header.Get("Authorization"); a != "" {
			auth = a
		}
		mu.Unlock()
		fmt.Fprint(w, "comfy-marker")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		see(w, r)
		fmt.Fprint(w, `{"prompt_id":"gw-test-1"}`)
	})
	for _, p := range []string{"/queue", "/history/", "/view", "/upload/"} {
		mux.HandleFunc(p, see)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	sock, b := daemon(t)
	g := &Gateway{Socket: sock, Ready: 5 * time.Second, Key: testKey,
		Engines: []config.Engine{{Name: "comfyui", Type: "comfyui", URL: srv.URL}}}
	gw := httptest.NewServer(g.Handler())
	t.Cleanup(gw.Close)

	for _, c := range []struct{ method, path, body, want string }{
		{"POST", "/prompt", `{"prompt":{"3":{"class_type":"KSampler"}}}`, `"prompt_id":"gw-test-1"`},
		{"GET", "/queue", "", "comfy-marker"},
		{"GET", "/history/gw-test-1", "", "comfy-marker"},
		{"GET", "/view?filename=out.png", "", "comfy-marker"},
		{"POST", "/upload/image", "bytes", "comfy-marker"},
	} {
		resp := call(t, c.method, gw.URL+c.path, c.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), c.want) {
			t.Fatalf("%s %s: %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}
	mu.Lock()
	got := auth
	mu.Unlock()
	if got != "" {
		t.Fatalf("ComfyUI received Authorization %q, want none", got)
	}
	if n := len(b.Status().Leases); n != 0 {
		t.Fatalf("%d leases for a ComfyUI that was already up", n)
	}
}

// A credential that will not resolve (an unset variable) sends no
// Authorization and logs one WARN line naming the engine and the error,
// never the key: the request goes through, and the engine refuses in its
// own words if it needs one.
func TestUnresolvableKeySendsNoneAndWarnsOnce(t *testing.T) {
	const env = "GW_TEST_MISSING_KEY"
	if os.Getenv(env) != "" {
		t.Skipf("%s is set in the test environment", env)
	}
	srv, saw := keyEngine(t, "llama3")
	sock, b := daemon(t)
	log := &syncBuf{}
	g := &Gateway{Socket: sock, Ready: 5 * time.Second, Key: testKey,
		Log: slog.New(slog.NewTextHandler(log, nil)),
		Engines: []config.Engine{{Name: "ollama", Type: "ollama", URL: srv.URL,
			Serves: []string{"llama3"}, APIKeyEnv: env}}}
	gw := httptest.NewServer(g.Handler())
	t.Cleanup(gw.Close)

	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"llama3"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "key-marker") {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if got := saw(); got != "" {
		t.Fatalf("the engine received Authorization %q, want none", got)
	}
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	warns := 0
	for _, l := range lines {
		if !strings.Contains(l, "engine key not resolved") {
			continue
		}
		warns++
		if !strings.Contains(l, `engine=ollama`) || !strings.Contains(l, env) {
			t.Fatalf("the WARN line does not name the engine and the cause: %s", l)
		}
		if strings.Contains(l, testKey) {
			t.Fatalf("the WARN line carries a key: %s", l)
		}
	}
	if warns != 1 {
		t.Fatalf("%d WARN lines for one request, want one:\n%s", warns, log.String())
	}
	if n := len(b.Status().Leases); n != 0 {
		t.Fatalf("%d leases for an engine that was already up", n)
	}
}
