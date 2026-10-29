// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package insights computes the request KPIs, the token ledger and the
// leaderboard from a request log. The node runs it over its in-memory 24 h
// log; the harbor's store runs it over the same rows persisted in SQLite,
// so both views of a range agree.
package insights

import (
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

// Gen is one delta of an engine's own counters, credited to the model it
// had loaded: decode speed and draft acceptance for engines whose request
// log does not report them. The node is the only source of these.
type Gen struct {
	T           time.Time
	Engine      string
	Model       string
	Out, OutSec float64
	Draft       float64
	DraftAcc    float64
}

// Col is one ledger column: a label and the window of requests it sums,
// counted back from now; a zero window means all of the requests given.
type Col struct {
	Label  string
	Window time.Duration
}

// Compute is the KPIs, ledger and leaderboard over reqs at now. KPIs use
// fixed windows (5 m, 15 m, 1 h) regardless of the range; the ledger uses
// the given columns; the leaderboard and the recent list cover all of
// reqs.
func Compute(reqs []proto.Request, gen []Gen, cols []Col, since, now time.Time) *proto.Insights {
	in := &proto.Insights{Since: since, Ledger: []proto.LedgerCol{}, Leaderboard: []proto.BoardRow{}, Recent: []proto.Request{}}
	within := func(d time.Duration) []proto.Request {
		var out []proto.Request
		for _, q := range reqs {
			if now.Sub(q.T) <= d {
				out = append(out, q)
			}
		}
		return out
	}
	last5, last15, hour := within(5*time.Minute), within(15*time.Minute), within(time.Hour)
	in.Requests5m = len(last5)
	in.RPM = float64(len(last5)) / 5

	var durs, decs, pres []float64
	var preMS float64
	var preN int
	for _, g := range gen {
		if now.Sub(g.T) <= 15*time.Minute {
			decs = append(decs, g.Out/g.OutSec)
		}
	}
	for _, q := range last15 {
		durs = append(durs, q.DurationMS)
		if q.DecodeTPS > 0 {
			decs = append(decs, q.DecodeTPS)
		}
		if q.PrefillTPS > 0 {
			pres = append(pres, q.PrefillTPS)
		}
		if q.PrefillMS > 0 {
			preMS += q.PrefillMS
			preN++
		}
	}
	in.P50MS, in.P95MS = pct(durs, 50), pct(durs, 95)
	in.DecodeTPS, in.PrefillTPS = pct(decs, 50), pct(pres, 50)
	if in.DecodeTPS > 0 {
		in.TPOTMS = 1000 / in.DecodeTPS
	}
	if preN > 0 {
		in.PrefillMS = preMS / float64(preN)
	}
	var prompt, cache, draft, acc float64
	for _, q := range hour {
		prompt += q.Prompt
		cache += q.Cache
		draft += q.Draft
		acc += q.DraftAcc
	}
	if prompt > 0 {
		in.CachePct = 100 * cache / prompt
		in.ReingestPct = 100 - in.CachePct
	}
	for _, g := range gen {
		if now.Sub(g.T) <= time.Hour {
			draft += g.Draft
			acc += g.DraftAcc
		}
	}
	if draft > 0 {
		in.MTPPct = 100 * acc / draft
	}

	for _, c := range cols {
		rs := reqs
		if c.Window > 0 {
			rs = within(c.Window)
		}
		l := proto.LedgerCol{Label: c.Label, Requests: len(rs)}
		for _, q := range rs {
			l.Generated += q.Output
			l.Cached += q.Cache
			if f := q.Prompt - q.Cache; f > 0 {
				l.Fresh += f
			}
		}
		in.Ledger = append(in.Ledger, l)
	}

	type key struct{ e, m string }
	rows := map[key]*proto.BoardRow{}
	decBy := map[key][]float64{}
	var cacheBy, draftBy, accBy = map[key]float64{}, map[key]float64{}, map[key]float64{}
	for _, q := range reqs {
		k := key{q.Engine, q.Model}
		r := rows[k]
		if r == nil {
			r = &proto.BoardRow{Engine: q.Engine, Model: q.Model}
			rows[k] = r
		}
		r.Requests++
		r.Prompt += q.Prompt
		r.Output += q.Output
		cacheBy[k] += q.Cache
		draftBy[k] += q.Draft
		accBy[k] += q.DraftAcc
		if q.DecodeTPS > 0 {
			decBy[k] = append(decBy[k], q.DecodeTPS)
		}
	}
	logged := map[key]bool{}
	for k := range rows {
		logged[k] = true
	}
	for _, g := range gen {
		k := key{g.Engine, g.Model}
		// An engine with no request log (llama.cpp) gets its row from the
		// counters alone: output and decode, with the request count unknown.
		if !logged[k] {
			if rows[k] == nil {
				rows[k] = &proto.BoardRow{Engine: g.Engine, Model: g.Model}
			}
			rows[k].Output += g.Out
		}
		decBy[k] = append(decBy[k], g.Out/g.OutSec)
		draftBy[k] += g.Draft
		accBy[k] += g.DraftAcc
	}
	for k, r := range rows {
		r.DecodeMedian = pct(decBy[k], 50)
		if r.Prompt > 0 {
			r.CachePct = 100 * cacheBy[k] / r.Prompt
		}
		if draftBy[k] > 0 {
			r.MTPPct = 100 * accBy[k] / draftBy[k]
		}
		in.Leaderboard = append(in.Leaderboard, *r)
	}
	sort.Slice(in.Leaderboard, func(i, j int) bool { return in.Leaderboard[i].Output > in.Leaderboard[j].Output })
	for i := len(reqs) - 1; i >= 0 && len(in.Recent) < 50; i-- {
		in.Recent = append(in.Recent, reqs[i])
	}
	return in
}

// pct is the p-th percentile (nearest rank) of xs; 0 for none.
func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(float64(len(s))*p/100+0.5) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}
