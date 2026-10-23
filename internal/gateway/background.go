// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

// Background engines. An engine with background = true runs whenever the
// card is free: a tagger, an indexer, a transcription queue. The gateway
// starts it at batch priority once the card has been free for the engine's
// idle_after; any other request drains it as it drains any batch lease, and
// it stays out until the card is free that long again, so a game or a hold
// keeps it out for as long as it lasts.

import (
	"context"
	"slices"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
)

// DefaultIdleAfter is how long the card must be free before a background
// engine starts, when its entry names no idle_after.
const DefaultIdleAfter = 60 * time.Second

// cardFree is whether nothing holds, uses or waits for the card and the
// card serves this machine. A parked lease holds it: a batch job must not
// evict an engine someone parked in RAM to come back to. A stowaway or a
// tracked app (Blender) holds memory no lease accounts for, and a start
// beside it is the collision Berth exists to prevent.
func cardFree(st *proto.Status) bool {
	if st == nil || st.Passthrough != nil || st.ProbeErr != "" ||
		len(st.Leases) > 0 || len(st.Waiting) > 0 || len(st.Squatters) > 0 {
		return false
	}
	// Status lists every claim rule; only one matching right now is in use.
	return !slices.ContainsFunc(st.Claims, func(c proto.Claim) bool { return c.Active })
}

// Background starts the background engines while ctx lives, checking the
// card every `every` (0 = 5 s). One background engine runs at a time, the
// first in berth.toml whose card has been free for its idle_after.
func (g *Gateway) Background(ctx context.Context, every time.Duration) {
	var bg []*config.Engine
	for i := range g.Engines {
		if g.Engines[i].Background {
			bg = append(bg, &g.Engines[i])
		}
	}
	if len(bg) == 0 {
		return
	}
	if every <= 0 {
		every = 5 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	var freeSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !cardFree(g.nodeStatus()) {
			freeSince = time.Time{}
			continue
		}
		now := time.Now()
		if freeSince.IsZero() {
			freeSince = now
		}
		for _, e := range bg {
			idle := e.IdleAfter.Duration
			if idle <= 0 {
				idle = DefaultIdleAfter
			}
			if now.Sub(freeSince) < idle {
				continue
			}
			g.log().Info("the card is free; starting a background engine", "engine", e.Name, "free_for", now.Sub(freeSince).Round(time.Second))
			freeSince = time.Time{}
			go func() {
				sctx, cancel := context.WithTimeout(ctx, g.ready())
				defer cancel()
				if err := g.ensure(sctx, e, false); err != nil {
					g.log().Warn("background engine did not start", "engine", e.Name, "err", err)
				}
			}()
			break
		}
	}
}
