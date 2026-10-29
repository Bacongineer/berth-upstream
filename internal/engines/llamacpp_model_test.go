// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
)

// A llama.cpp server names its model in /props and keeps no request log.
// Berth lists the model, and its generated tokens get a leaderboard row
// from the counters, with the request count left unknown.
func TestLlamaCppModelReachesTheLeaderboard(t *testing.T) {
	out, sec := 1000.0, 10.0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/metrics":
			fmt.Fprintf(w, "llamacpp:tokens_predicted_total %g\nllamacpp:tokens_predicted_seconds_total %g\nllamacpp:requests_processing 1\n", out, sec)
		case "/props":
			fmt.Fprint(w, `{"default_generation_settings":{"n_ctx":32768},"model_alias":"Qwen3-8B-Q4_K_M","n_ctx":32768}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	w := &Watcher{Engines: []config.Engine{{Name: "llama", Type: "llamacpp", URL: srv.URL}}}
	w.Poll(context.Background())
	out, sec = 1500, 15
	w.Poll(context.Background())

	st := w.Snapshot()
	if len(st) != 1 || len(st[0].Models) != 1 || st[0].Models[0].Name != "Qwen3-8B-Q4_K_M" || st[0].Models[0].State != "loaded" {
		t.Fatalf("engine = %+v, want Qwen3-8B-Q4_K_M loaded", st)
	}
	in := w.Insights(time.Now())
	if in == nil || len(in.Leaderboard) != 1 {
		t.Fatalf("insights = %+v", in)
	}
	r := in.Leaderboard[0]
	if got := fmt.Sprintf("%s/%s requests=%d output=%g decode=%g", r.Engine, r.Model, r.Requests, r.Output, r.DecodeMedian); got != "llama/Qwen3-8B-Q4_K_M requests=0 output=500 decode=100" {
		t.Fatalf("row = %s", got)
	}
}
