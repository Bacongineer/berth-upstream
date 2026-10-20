// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
)

func TestStrataAuthenticatedCountersAndSlots(t *testing.T) {
	t.Setenv("BERTH_STRATA_TEST_KEY", "fixture-secret")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			if r.Header.Get("Authorization") != "" {
				t.Error("the open health probe received a credential")
			}
			fmt.Fprint(w, `{"loaded":true,"model":"fixture-strata","max_context":32768}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/metrics":
			fmt.Fprint(w, `{"live":{"state":"generating","queued":3},"totals":{"prompt_tokens":123,"output_tokens":45,"prompt_ms":200,"decode_ms":300}}`)
		case "/slots":
			fmt.Fprint(w, `[{"id":0,"n_ctx":32768,"is_processing":true,"n_prompt_tokens":123}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	watcher := &Watcher{Engines: []config.Engine{{Name: "strata", Type: "strata", URL: srv.URL, APIKeyEnv: "BERTH_STRATA_TEST_KEY"}}}
	watcher.Poll(context.Background())
	got := watcher.Snapshot()[0]
	if !got.Up || got.Metrics.OutputTokens != 45 || got.Metrics.PromptTokens != 123 || got.Metrics.Waiting != 3 {
		t.Fatalf("authenticated Strata counters: %+v", got)
	}
	if len(got.Slots) != 1 || !got.Slots[0].Processing || got.Slots[0].Used != 123 {
		t.Fatalf("authenticated Strata slots: %+v", got.Slots)
	}
}
