// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/insights"
	"github.com/T-Crypt/berth/internal/proto"
)

// The request log: what the engines say each request did, kept 24 h in
// memory (by design for now). It is what the KPIs, the token
// ledger and the leaderboard are computed from.
const (
	keepRequests = 24 * time.Hour
	maxRequests  = 50000
)

// Insights is computed at most once per insightsEvery: every status read
// inside it gets the last result. With no new request or generation sample
// the result stands for insightsIdle, since only the time windows moved.
const (
	insightsEvery = 5 * time.Second
	insightsIdle  = time.Minute
)

// activity reads llama-swap's per-request log and adds the ones not seen yet.
func (w *Watcher) activity(ctx context.Context, engine, base string) error {
	b, err := w.get(ctx, base+"/api/metrics/activity")
	if err != nil {
		return err
	}
	var r struct {
		Data []struct {
			ID        json.Number `json:"id"`
			Timestamp time.Time   `json:"timestamp"`
			Model     string      `json:"model"`
			Status    int         `json:"resp_status_code"`
			Tokens    struct {
				Cache    float64 `json:"cache_tokens"`
				Draft    float64 `json:"draft_tokens"`
				DraftAcc float64 `json:"draft_acc_tokens"`
				Input    float64 `json:"input_tokens"`
				Output   float64 `json:"output_tokens"`
				Prefill  float64 `json:"prompt_per_second"`
				Decode   float64 `json:"tokens_per_second"`
			} `json:"tokens"`
			DurationMS float64 `json:"duration_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("/api/metrics/activity: %w", err)
	}
	var rows []proto.Request
	for _, d := range r.Data {
		// llama-swap's input_tokens is the uncached part of the prompt, and
		// -1 means the engine did not report that figure: it reads as 0 here
		// and is left out of every average.
		k := func(v float64) float64 { return max(v, 0) }
		t := d.Tokens
		q := proto.Request{
			ID: engine + "/" + d.ID.String(), Engine: engine, Model: d.Model, T: d.Timestamp, Status: d.Status,
			Prompt: k(t.Input) + k(t.Cache), Cache: k(t.Cache), Output: k(t.Output),
			Draft: k(t.Draft), DraftAcc: k(t.DraftAcc),
			PrefillTPS: k(t.Prefill), DecodeTPS: k(t.Decode), DurationMS: k(d.DurationMS),
		}
		if fresh := k(t.Input); fresh > 0 && q.PrefillTPS > 0 {
			q.PrefillMS = 1000 * fresh / q.PrefillTPS
		}
		rows = append(rows, q)
	}
	w.record(rows)
	return nil
}

// record adds an engine's request rows the log has not seen, keeps the log
// in time order, and drops what is past 24 h or the cap.
func (w *Watcher) record(rows []proto.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = map[string]bool{}
		w.since = time.Now()
	}
	added := false
	for _, q := range rows {
		if w.seen[q.ID] {
			continue
		}
		added = true
		w.seen[q.ID] = true
		w.reqs = append(w.reqs, q)
	}
	if added {
		w.insDirty = true
		sort.SliceStable(w.reqs, func(i, j int) bool { return w.reqs[i].T.Before(w.reqs[j].T) })
	}
	cut := time.Now().Add(-keepRequests)
	i := 0
	for i < len(w.reqs) && (w.reqs[i].T.Before(cut) || len(w.reqs)-i > maxRequests) {
		delete(w.seen, w.reqs[i].ID)
		i++
	}
	w.reqs = w.reqs[i:]
}

// Insights computes the KPIs, ledger and leaderboard at now. The math is
// shared with the harbor's store (internal/insights); the node adds the
// engines' counter deltas, which the store never sees.
// The result is shared by every caller until the next compute, so callers
// only read it.
func (w *Watcher) Insights(now time.Time) *proto.Insights {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.since.IsZero() {
		return nil
	}
	if age := now.Sub(w.insAt); w.ins != nil && age >= 0 && (age < insightsEvery || !w.insDirty && age < insightsIdle) {
		return w.ins
	}
	gen := make([]insights.Gen, len(w.gen))
	for i, g := range w.gen {
		gen[i] = insights.Gen{T: g.t, Engine: g.engine, Model: g.model,
			Out: g.out, OutSec: g.outSec, Draft: g.draft, DraftAcc: g.draftAccepted}
	}
	cols := []insights.Col{{Label: "1h", Window: time.Hour}, {Label: "24h", Window: keepRequests}}
	cols = append(cols, insights.Col{Label: "since " + w.since.Format("Jan 2 15:04")})
	w.ins = insights.Compute(w.reqs, gen, cols, w.since, now)
	w.insAt, w.insDirty = now, false
	return w.ins
}

// Requests is the in-memory request log with T after since, oldest first.
// Since is the harbor's resume point: it stores the newest T it has, so an
// equal T is not resent.
func (w *Watcher) Requests(since time.Time) []proto.Request {
	if w == nil {
		return []proto.Request{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []proto.Request
	for _, q := range w.reqs {
		if q.T.After(since) {
			out = append(out, q)
		}
	}
	return out
}
