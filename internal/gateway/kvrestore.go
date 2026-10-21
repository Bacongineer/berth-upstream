// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// The gateway's half of KV snapshots: an engine this gateway starts on
// demand, and only such an engine, gets its saved prompt caches back before
// the first request. While the start waits for the card, the snapshots are
// warmed into the page cache beside the model files' prefetch; once the
// engine answers, its empty slots restore the newest snapshot of their
// model. Both halves are riders: a failure is one line, never a failed
// start or a delayed request.

package gateway

import (
	"context"
	"errors"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/httpauth"
	"github.com/T-Crypt/berth/internal/kvsnap"
)

var kvRestoreTimeout = 30 * time.Second

// kvWarm is fired beside the prefetch in a start: it waits for the engine
// to name its one model on /v1/models, then asks the kernel to read that
// model's snapshot files into the page cache, so the restore reads RAM
// where the kernel kept the pages. It waits as long as the start itself
// may; a platform without the page-cache interface says so once, and
// warming stays best effort.
func (g *Gateway) kvWarm(e *config.Engine, wait time.Duration) {
	bearer, ok := g.kvBearer(e)
	if !ok {
		return
	}
	idx, err := kvsnap.New(e.KVSnapshots)
	if err != nil {
		g.log().Debug("kv snapshot ledger did not open", "engine", e.Name, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(httpauth.WithBearer(context.Background(), bearer), wait)
	defer cancel()
	for {
		model, err := kvsnap.Model(ctx, e.URL)
		switch {
		case err == nil:
			key := kvsnap.Key{Engine: e.Name, Model: model}
			if err := idx.Warm(key); err != nil {
				// A page cache the kernel does not expose here is no
				// mistake to fix; the restore then reads the SSD once.
				g.log().Debug("kv snapshots not warmed", "engine", e.Name, "err", err)
			}
			return
		case errors.Is(err, kvsnap.ErrNoModel):
			// llama-swap lists every model it can load: no identity, no
			// snapshot, by design.
			g.log().Debug("kv snapshots skip the engine", "engine", e.Name, "err", err)
			return
		case ctx.Err() != nil:
			return // the engine never named a model during its start
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// kvRestore runs once the just-started engine answers, before ensure
// returns: every slot the ledger holds for the engine's model restores its
// newest snapshot. Nothing for another model is in the ledger's answer, so
// a snapshot never crosses models.
func (g *Gateway) kvRestore(e *config.Engine) {
	bearer, ok := g.kvBearer(e)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(httpauth.WithBearer(context.Background(), bearer), kvRestoreTimeout)
	defer cancel()
	log := g.log().With("engine", e.Name)
	idx, err := kvsnap.New(e.KVSnapshots)
	if err != nil {
		log.Warn("kv snapshot ledger did not open", "err", err)
		return
	}
	model, err := kvsnap.Model(ctx, e.URL)
	if err != nil {
		log.Debug("kv snapshots skipped, no identity", "err", err)
		return
	}
	ss, err := idx.For(kvsnap.Key{Engine: e.Name, Model: model})
	if err != nil {
		log.Warn("kv snapshot ledger did not read", "err", err)
		return
	}
	for _, s := range ss {
		r, err := kvsnap.Restore(ctx, e.URL, s.Slot, s.File)
		if err != nil {
			if errors.Is(err, kvsnap.ErrUnsupported) {
				// The engine started without --slot-save-path; the warm
				// cache rides out with it.
				log.Info("engine started without --slot-save-path; its slots stay unrestored")
				return
			}
			log.Warn("kv snapshot restore failed", "slot", s.Slot, "file", s.File, "err", err)
			continue
		}
		log.Info("kv snapshot restored", "slot", s.Slot, "tokens", r.Tokens, "took", r.Took)
	}
}

// kvBearer resolves the engine's own credential, the one its slot calls
// answer to; the gateway re-reads it on every restore, as the runs do.
func (g *Gateway) kvBearer(e *config.Engine) (string, bool) {
	bearer, err := e.APIKey()
	if err != nil {
		// A credential that will not resolve is the entry's problem, and
		// the engine will refuse the call in its own words.
		g.log().Debug("kv snapshots without a credential", "engine", e.Name, "err", err)
		return "", false
	}
	return bearer, true
}
