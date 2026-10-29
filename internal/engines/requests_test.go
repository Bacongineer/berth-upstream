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

func TestRequestLogKPIsLedgerLeaderboard(t *testing.T) {
	now := time.Now()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	body := fmt.Sprintf(`{"data":[
 {"id":3,"timestamp":%q,"model":"NInfer","resp_status_code":200,"tokens":{"cache_tokens":900,"draft_tokens":100,"draft_acc_tokens":80,"input_tokens":100,"output_tokens":200,"prompt_per_second":1000,"tokens_per_second":100},"duration_ms":3000},
 {"id":2,"timestamp":%q,"model":"NInfer","resp_status_code":200,"tokens":{"cache_tokens":0,"draft_tokens":-1,"draft_acc_tokens":-1,"input_tokens":1000,"output_tokens":100,"prompt_per_second":2000,"tokens_per_second":120},"duration_ms":1000},
 {"id":1,"timestamp":%q,"model":"Fast","resp_status_code":200,"tokens":{"input_tokens":10,"output_tokens":50,"prompt_per_second":500,"tokens_per_second":300},"duration_ms":500}]}`,
		ts(time.Minute), ts(2*time.Minute), ts(3*time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/running":
			fmt.Fprint(w, `{"running":[]}`)
		case "/api/metrics/activity":
			fmt.Fprint(w, body)
		}
	}))
	defer srv.Close()
	w := &Watcher{Engines: []config.Engine{{Name: "swap", Type: "llama-swap", URL: srv.URL}}}
	w.Poll(context.Background())
	w.Poll(context.Background()) // the same records again must not double count

	in := w.Insights(now)
	if in == nil || in.Requests5m != 2 || in.RPM != 0.4 {
		t.Fatalf("rate: %+v", in)
	}
	if in.P95MS != 3000 || in.P50MS != 1000 || in.DecodeTPS != 100 || in.TPOTMS != 10 {
		t.Fatalf("latency: p50 %v p95 %v decode %v tpot %v", in.P50MS, in.P95MS, in.DecodeTPS, in.TPOTMS)
	}
	// last hour: prompt 2000, cached 900 -> 45%; drafted 100, accepted 80 -> 80%
	if in.CachePct != 45 || in.ReingestPct != 55 || in.MTPPct != 80 {
		t.Fatalf("cache %v reingest %v mtp %v", in.CachePct, in.ReingestPct, in.MTPPct)
	}
	// prefill: (1000-900)/1000 s = 100 ms and 1000/2000 s = 500 ms -> mean 300
	if in.PrefillMS != 300 {
		t.Fatalf("prefill ms %v", in.PrefillMS)
	}
	h, d := in.Ledger[0], in.Ledger[1]
	if h.Requests != 2 || h.Generated != 300 || h.Cached != 900 || h.Fresh != 1100 || d.Requests != 3 || d.Generated != 350 {
		t.Fatalf("ledger %+v", in.Ledger)
	}
	if len(in.Leaderboard) != 2 || in.Leaderboard[0].Model != "NInfer" || in.Leaderboard[0].Requests != 2 || in.Leaderboard[0].MTPPct != 80 {
		t.Fatalf("leaderboard %+v", in.Leaderboard)
	}
	if len(in.Recent) != 3 || in.Recent[0].ID != fmt.Sprintf("swap/3@%d", now.Add(-time.Minute).Unix()) {
		t.Fatalf("recent %+v", in.Recent)
	}
}

// TestWatcherRequestsSince is the harbor's resume point: everything after
// since, oldest first, and a since at the newest t sends nothing (an equal
// t is not resent, so the harbor never re-reads the same request).
func TestWatcherRequestsSince(t *testing.T) {
	now := time.Now()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	body := fmt.Sprintf(`{"data":[
 {"id":3,"timestamp":%q,"model":"NInfer","resp_status_code":200,"tokens":{"input_tokens":100,"output_tokens":200,"prompt_per_second":1000,"tokens_per_second":100},"duration_ms":3000},
 {"id":2,"timestamp":%q,"model":"NInfer","resp_status_code":200,"tokens":{"input_tokens":1000,"output_tokens":100,"prompt_per_second":2000,"tokens_per_second":120},"duration_ms":1000},
 {"id":1,"timestamp":%q,"model":"Fast","resp_status_code":200,"tokens":{"input_tokens":10,"output_tokens":50,"prompt_per_second":500,"tokens_per_second":300},"duration_ms":500}]}`,
		ts(time.Minute), ts(2*time.Minute), ts(3*time.Hour))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/running":
			fmt.Fprint(w, `{"running":[]}`)
		case "/api/metrics/activity":
			fmt.Fprint(w, body)
		}
	}))
	defer srv.Close()
	w := &Watcher{Engines: []config.Engine{{Name: "swap", Type: "llama-swap", URL: srv.URL}}}
	w.Poll(context.Background())

	all := w.Requests(time.Time{})
	id := func(n int, ago time.Duration) string { return fmt.Sprintf("swap/%d@%d", n, now.Add(-ago).Unix()) }
	if len(all) != 3 || all[0].ID != id(1, 3*time.Hour) || all[2].ID != id(3, time.Minute) {
		t.Fatalf("all: %+v", all)
	}
	mid := w.Requests(all[0].T)
	if len(mid) != 2 || mid[0].ID != id(2, 2*time.Minute) || mid[1].ID != id(3, time.Minute) {
		t.Fatalf("after the oldest: %+v", mid)
	}
	if got := w.Requests(all[2].T); len(got) != 0 {
		t.Fatalf("an equal t must not be resent: %+v", got)
	}
	if got := (*Watcher)(nil).Requests(time.Time{}); len(got) != 0 {
		t.Fatalf("nil watcher: %+v", got)
	}
}

// Insights is computed once per interval, not once per status read: a
// second read inside it allocates nothing. A new request shows after the
// interval, and with nothing new the result stands for a minute, because
// only the time windows moved.
func TestInsightsComputedOncePerInterval(t *testing.T) {
	now := time.Now()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	rec := func(id int, ago time.Duration) string {
		return fmt.Sprintf(`{"id":%d,"timestamp":%q,"model":"NInfer","resp_status_code":200,"tokens":{"input_tokens":10,"output_tokens":20,"prompt_per_second":100,"tokens_per_second":50},"duration_ms":400}`, id, ts(ago))
	}
	body := `{"data":[` + rec(1, 4*time.Minute+50*time.Second) + `]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/running":
			fmt.Fprint(w, `{"running":[]}`)
		case "/api/metrics/activity":
			fmt.Fprint(w, body)
		}
	}))
	defer srv.Close()
	w := &Watcher{Engines: []config.Engine{{Name: "swap", Type: "llama-swap", URL: srv.URL}}}
	w.Poll(context.Background())

	if in := w.Insights(now); in == nil || in.Requests5m != 1 {
		t.Fatalf("first read: %+v", in)
	}
	if n := testing.AllocsPerRun(10, func() { w.Insights(now.Add(2 * time.Second)) }); n != 0 {
		t.Fatalf("a read inside the interval allocated %v times, want 0", n)
	}

	body = `{"data":[` + rec(2, 0) + `,` + rec(1, 4*time.Minute+50*time.Second) + `]}`
	w.Poll(context.Background())
	if in := w.Insights(now.Add(6 * time.Second)); in.Requests5m != 2 {
		t.Fatalf("after the interval: %d requests in 5m, want 2", in.Requests5m)
	}

	// Request 1 leaves the 5 min window at +10 s. Nothing new arrives, so
	// the result stands until a minute after the last compute.
	if in := w.Insights(now.Add(30 * time.Second)); in.Requests5m != 2 {
		t.Fatalf("idle inside a minute: %d requests in 5m, want the standing 2", in.Requests5m)
	}
	if in := w.Insights(now.Add(70 * time.Second)); in.Requests5m != 1 {
		t.Fatalf("idle after a minute: %d requests in 5m, want 1", in.Requests5m)
	}
}

// llama-swap numbers its activity from 1 again after a restart. A request
// after the restart is a new row, not the old one with the same number:
// the harbor stores rows by ID and ignores one it has, so a reused ID
// dropped every request after a restart from its history.
func TestRequestIDsSurviveALlamaSwapRestart(t *testing.T) {
	after := time.Now().Truncate(time.Second).Add(-time.Minute)
	before := after.Add(-3 * time.Hour)
	body := fmt.Sprintf(`{"data":[{"id":232,"timestamp":%q,"model":"Qwen3.6-35B-A3B-IQ4-Agent","resp_status_code":200,"tokens":{"output_tokens":10}}]}`, before.Format(time.RFC3339))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/running":
			fmt.Fprint(w, `{"running":[]}`)
		case "/api/metrics/activity":
			fmt.Fprint(w, body)
		}
	}))
	defer srv.Close()
	w := &Watcher{Engines: []config.Engine{{Name: "llama-swap", Type: "llama-swap", URL: srv.URL}}}
	w.Poll(context.Background())
	body = fmt.Sprintf(`{"data":[{"id":232,"timestamp":%q,"model":"NInfer-Qwen3.8-27B","resp_status_code":200,"tokens":{"output_tokens":1}}]}`, after.Format(time.RFC3339))
	w.Poll(context.Background())

	got := w.Requests(time.Time{})
	if len(got) != 2 || got[0].ID != fmt.Sprintf("llama-swap/232@%d", before.Unix()) ||
		got[1].ID != fmt.Sprintf("llama-swap/232@%d", after.Unix()) || got[1].Model != "NInfer-Qwen3.8-27B" {
		t.Fatalf("requests = %+v, want both 232s as two rows", got)
	}
}
