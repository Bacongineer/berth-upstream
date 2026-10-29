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
	"github.com/T-Crypt/berth/internal/proto"
)

// strataServer answers /health with the model it serves and /metrics with
// Strata 0.1.45's request list, at whatever port httptest picks.
func strataServer(t *testing.T, model string, at time.Time, finish string) *httptest.Server {
	t.Helper()
	unix := float64(at.UnixMicro()) / 1e6
	metrics := fmt.Sprintf(`{"engine":{"model":%q},"live":{"state":"idle","queued":0},
 "requests":[{"time":%f,"duration_s":4.7,"finish":%q,"prompt_tokens":32000,"reused":31552,"output_tokens":388,
  "prompt_ms":621.1,"decode_ms":4064.9,"decode_tok_s":95.5,"drafts_offered":277,"drafts_accepted":214}],
 "totals":{"prompt_tokens":32000,"output_tokens":388,"prompt_ms":621.1,"decode_ms":4064.9}}`, model, unix, finish)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprintf(w, `{"status":"ok","model":%q,"loaded":true,"max_context":262144,"service":"strata"}`, model)
		case "/metrics":
			fmt.Fprint(w, metrics)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Two Strata engines serving two quants each get their own leaderboard row
// under the model their /health names, whatever port they run on, and a
// second poll of the same request list counts nothing twice.
func TestStrataRequestsReachTheLeaderboard(t *testing.T) {
	at := time.Date(2026, 10, 28, 21, 35, 21, 155911000, time.Local)
	iq4 := strataServer(t, "qwen3.8-flash-next-ud-iq4_xs", at, "stop")
	sc117 := strataServer(t, "qwen3.8-flash-next-sc117-iq3_s", at.Add(time.Second), "cancel")
	w := &Watcher{Engines: []config.Engine{
		{Name: "strata", Type: "strata", URL: iq4.URL},
		{Name: "strata-sc117", Type: "strata", URL: sc117.URL},
	}}
	w.Poll(context.Background())
	w.Poll(context.Background())

	got := w.Requests(time.Time{})
	want := []proto.Request{
		{ID: "strata/1793244921.155911", Engine: "strata", Model: "qwen3.8-flash-next-ud-iq4_xs", T: at, Status: 200,
			Prompt: 32000, Cache: 31552, Output: 388, Draft: 277, DraftAcc: 214,
			PrefillTPS: 448 / 0.6211, DecodeTPS: 95.5, DurationMS: 4700, PrefillMS: 621.1},
		{ID: "strata-sc117/1793244922.155911", Engine: "strata-sc117", Model: "qwen3.8-flash-next-sc117-iq3_s", T: at.Add(time.Second), Status: 499,
			Prompt: 32000, Cache: 31552, Output: 388, Draft: 277, DraftAcc: 214,
			PrefillTPS: 448 / 0.6211, DecodeTPS: 95.5, DurationMS: 4700, PrefillMS: 621.1},
	}
	if len(got) != len(want) {
		t.Fatalf("requests = %+v, want %+v", got, want)
	}
	for i := range want {
		if !got[i].T.Equal(want[i].T) {
			t.Errorf("request %d at %v, want %v", i, got[i].T, want[i].T)
		}
		got[i].T = want[i].T
		if got[i] != want[i] {
			t.Errorf("request %d = %+v\nwant %+v", i, got[i], want[i])
		}
	}

	rows := map[string]string{}
	for _, r := range w.Insights(at.Add(time.Minute)).Leaderboard {
		rows[r.Engine] = fmt.Sprintf("%s requests=%d output=%g", r.Model, r.Requests, r.Output)
	}
	if rows["strata"] != "qwen3.8-flash-next-ud-iq4_xs requests=1 output=388" ||
		rows["strata-sc117"] != "qwen3.8-flash-next-sc117-iq3_s requests=1 output=388" {
		t.Fatalf("leaderboard = %v", rows)
	}
}
