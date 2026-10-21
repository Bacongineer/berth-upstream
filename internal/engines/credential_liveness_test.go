// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
)

// credentialFixtureBody returns the fixture answer for an endpoint, and
// whether the endpoint exists at all.
func credentialFixtureBody(path string) (string, bool) {
	switch path {
	case "/queue":
		return `{"queue_running":[[1]],"queue_pending":[[2],[3]]}`, true
	case "/system_stats":
		return `{"system":{"comfyui_version":"fixture"}}`, true
	case "/v1/models":
		return `{"data":[{"id":"fixture-model","max_model_len":2048}]}`, true
	case "/metrics":
		return "fixture:prompt_tokens_total 10\nfixture:generation_tokens_total 5\n", true
	case "/api/v1/model":
		return `{"result":"fixture-kobold"}`, true
	case "/api/v1/config/max_length":
		return `{"value":4096}`, true
	case "/tabby/config":
		return `{"context_length":8192}`, true
	case "/api/v0/models":
		return `{"data":[{"id":"fixture-lm","state":"loaded","max_context_length":1024,"loaded_context_length":1024}]}`, true
	case "/v1/stats":
		return `{"generated_tokens":5}`, true
	case "/health":
		return `{"status":"healthy","loaded":true,"model":"fixture"}`, true
	case "/v1/audio/voices":
		return `{"voices":[[1]],"default":"fixture"}`, true
	case "/api/health":
		return `{"status":"healthy"}`, true
	case "/api/ps":
		return `{"models":[{"name":"fixture","size_vram":1}]}`, true
	case "/running":
		return `{"running":[{"model":"fixture","state":"ready","proxy":""}]}`, true
	case "/props":
		return `{"n_ctx":4096}`, true
	case "/slots":
		return `[{"id":0,"state":"idle","n_ctx":4096}]`, true
	case "/":
		return `ok`, true
	}
	return "", false
}

func newCredentialFixtureServer(t *testing.T, serverToken string, guarded map[string]bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, known := credentialFixtureBody(r.URL.Path)
		if !known {
			http.NotFound(w, r)
			return
		}
		if guarded[r.URL.Path] && r.Header.Get("Authorization") != "Bearer "+serverToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, body)
	}))
}

func TestCredentialFailureNeverDecidesLiveness(t *testing.T) {
	types := []string{"comfyui", "openai", "vllm", "sglang", "localai", "koboldcpp",
		"tabbyapi", "lmstudio", "freetoken", "kokoro", "ollama", "llama-swap",
		"llamacpp", "strata", "unsloth", "custom"}
	// These readers send their liveness read with the credential, so an
	// accepted credential must yield a clean card. The others read openly
	// by design; on a guarded server they are up with the refusal shown.
	usesCredential := map[string]bool{"comfyui": true, "openai": true, "vllm": true,
		"sglang": true, "localai": true, "koboldcpp": true, "tabbyapi": true,
		"lmstudio": true, "freetoken": true, "kokoro": true, "custom": true}

	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "key")
	for _, typ := range types {
		t.Run(typ, func(t *testing.T) {
			engine := config.Engine{Name: "fixture-" + typ, Type: typ, URL: ""}
			if typ == "custom" {
				engine.Health = "/health"
				engine.Map = map[string]string{
					"prompt_tokens":  "fixture:prompt_tokens_total",
					"output_tokens":  "fixture:generation_tokens_total",
					"prompt_seconds": "fixture:prompt_seconds_total",
					"output_seconds": "fixture:generation_seconds_total",
					"processing":     "fixture:requests_running",
					"waiting":        "fixture:requests_waiting",
				}
			}

			// Scenario 1: the engine answers openly and the credential
			// (when used) is accepted.
			open := newCredentialFixtureServer(t, "unused", nil)
			e := engine
			e.URL = open.URL
			got := (&Watcher{}).read(context.Background(), e)
			if !got.Up || got.Error != "" {
				t.Fatalf("open engine: up=%v error=%q", got.Up, got.Error)
			}
			open.Close()

			// The same fixture with the credential accepted by the server.
			valid := newCredentialFixtureServer(t, "good-token", map[string]bool{
				"/queue": true, "/v1/models": true, "/metrics": true, "/api/v1/model": true,
				"/tabby/config": true, "/api/v0/models": true, "/v1/stats": true,
				"/v1/audio/voices": true, "/api/ps": true, "/running": true,
			})
			e = engine
			e.URL = valid.URL
			e.APIKeyFile = keyPath
			if err := os.WriteFile(keyPath, []byte("good-token"), 0600); err != nil {
				t.Fatal(err)
			}
			got = (&Watcher{}).read(context.Background(), e)
			if !got.Up {
				t.Fatalf("accepted credential marked engine down: error=%q", got.Error)
			}
			if usesCredential[typ] && got.Error != "" {
				t.Fatalf("accepted credential reported an error: %q", got.Error)
			}

			// Scenario 2: the credential is readable and the server
			// refuses it. The refusal is the server's own answer: the
			// engine is up; the credential problem is its error.
			e = engine
			e.URL = valid.URL
			if err := os.WriteFile(keyPath, []byte("wrong-token"), 0600); err != nil {
				t.Fatal(err)
			}
			got = (&Watcher{}).read(context.Background(), e)
			if !got.Up {
				t.Fatalf("refused credential marked engine down: error=%q", got.Error)
			}
			// Strata's contract (TestStrataKeyedInstallStaysUp) is a
			// silent best-effort on refused optional telemetry: health
			// decides and a refusal there is not an engine error. Every
			// other type's deciding read is credential-dependent on the
			// guarded fixture, so its refusal must show.
			if typ != "strata" && got.Error == "" {
				t.Fatalf("refused credential was not reported: up=%v", got.Up)
			}

			// Scenario 3: the credential file cannot be read, so no
			// authenticated request goes out. An unauthenticated
			// answer still proves liveness.
			e = engine
			e.URL = valid.URL
			e.APIKeyFile = keyPath
			if err := os.Remove(keyPath); err != nil {
				t.Fatal(err)
			}
			got = (&Watcher{}).read(context.Background(), e)
			if !got.Up {
				t.Fatalf("unreadable credential marked engine down: error=%q", got.Error)
			}
			if got.Error == "" {
				t.Fatalf("unreadable credential was not reported: up=%v", got.Up)
			}
			valid.Close()

			// Scenario 4: nothing answers. The engine is down.
			down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			url := down.URL
			down.Close()
			e = engine
			e.URL = url
			e.APIKeyFile = ""
			got = (&Watcher{}).read(context.Background(), e)
			if got.Up {
				t.Fatalf("unreachable engine read as up: error=%q", got.Error)
			}
		})
	}
}
