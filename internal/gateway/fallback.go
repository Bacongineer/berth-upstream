// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Fallback routing: an engine may name another engine to take one request
// while it is still coming up, so an interactive client does not sit
// through a 90 s cold start when something that needs no card can answer
// now. Opt-in per engine, one hop, and the primary's start is never
// cancelled by it.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/T-Crypt/berth/internal/config"
)

// defaultFallbackAfter is how long a request waits for its engine before
// it may go to the fallback, when the engine names one but sets no time.
const defaultFallbackAfter = 10 * time.Second

func (g *Gateway) fallbackAfter(e *config.Engine) time.Duration {
	if e.FallbackAfter.Duration > 0 {
		return e.FallbackAfter.Duration
	}
	return defaultFallbackAfter
}

// waitPrimary runs ensure for the engine a request resolved to, and waits
// it out. When the engine names a fallback, the wait watches the clock:
// the engine still not answering after fallback_after, and the fallback
// answering its probe right then, this request goes to the fallback
// instead. The probe is all the gateway ever sends a fallback it did not
// see up: one that is not answering means no fallback, and the request
// keeps waiting for the primary as today. ensure is never cancelled
// either way, so the primary's start goes on and later requests land on
// it.
//
// The return says where the request goes: fb nil means the primary (err
// is ensure's outcome); fb set means proxy it there, waited long. A
// streaming response (w non-nil) gets its loading comments every
// keepAliveEvery while it waits, as before.
func (g *Gateway) waitPrimary(ctx context.Context, e *config.Engine, w http.ResponseWriter) (fb *config.Engine, waited time.Duration, err error) {
	start := time.Now()
	// A request that switches to the fallback returns without its engine's
	// wait being over, and the handler's context goes with it: the wait for
	// the primary rides on one that survives, so the start is never
	// cancelled and answering late still counts for the next request.
	ensureCtx := ctx
	if e.Fallback != "" {
		ensureCtx = context.WithoutCancel(ctx)
	}
	ch := make(chan error, 1)
	go func() { ch <- g.ensure(ensureCtx, e, false) }()
	var fbAt <-chan time.Time
	if e.Fallback != "" {
		at := time.NewTimer(g.fallbackAfter(e))
		defer at.Stop()
		fbAt = at.C // a Timer fires once: the fallback gets one look
	}
	var tick <-chan time.Time
	if w != nil {
		every := time.NewTicker(keepAliveEvery)
		defer every.Stop()
		tick = every.C
	}
	for {
		select {
		case err := <-ch:
			return nil, time.Since(start), err
		case <-ctx.Done():
			return nil, time.Since(start), ctx.Err()
		case <-tick:
			fmt.Fprintf(w, ": berth: loading %s, %d s\n\n", e.Name, int64(time.Since(start).Seconds()))
			_ = http.NewResponseController(w).Flush()
		case <-fbAt:
			f := g.engineByName(e.Fallback)
			if f != nil && g.answers(ctx, f.URL) {
				return f, time.Since(start), nil
			}
		}
	}
}

// tookFallback hands one request to the fallback: it journals the switch,
// rewrites the body's model when the fallback lists it under another
// name, and marks the response, a header while the headers are still open
// and an SSE comment once the preamble is already on the wire. It returns
// the base URL to proxy to and the body to send.
func (g *Gateway) tookFallback(w http.ResponseWriter, preamble bool, e, fb *config.Engine, waited time.Duration, body []byte) (string, []byte) {
	if e.FallbackModel != "" {
		body = rewriteModel(body, e.FallbackModel)
	}
	g.log().Info("gateway request fell back", "engine", e.Name, "fallback", fb.Name, "waited", waited.Round(time.Millisecond))
	if preamble {
		fmt.Fprintf(w, ": berth: fallback %s\n\n", fb.Name)
		_ = http.NewResponseController(w).Flush()
	} else {
		w.Header().Set("X-Berth-Fallback", fb.Name)
	}
	return fb.URL, body
}

// rewriteModel replaces the request body's model name. Everything else in
// the body goes through byte for byte.
func rewriteModel(body []byte, model string) []byte {
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	name, _ := json.Marshal(model)
	req["model"] = name
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}
