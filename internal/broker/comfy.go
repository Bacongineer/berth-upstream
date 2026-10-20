// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Unprotected ComfyUI runs. A waiter never loads onto weights that would die;
// it asks the squatting server to unload through its own /free and waits.

import (
	"context"
	"time"

	"github.com/T-Crypt/berth/internal/comfy"
)

// ComfyEngine names one configured ComfyUI engine: the unprotected run
// name berth run records when a keep-up drain leaves the server behind,
// and the base URL its /free lives on.
type ComfyEngine struct {
	Name string
	URL  string
}

const (
	// comfySquatMin is the weight of an unprotected ComfyUI that is worth
	// freeing before a grant: an idle unloaded server holds a few hundred
	// MiB and never blocks one.
	comfySquatMin = 1 << 30
	// comfyPokeEvery rate limits the unload ask per engine while a waiter
	// is blocked. ComfyUI applies /free at its job boundary, so a busy
	// server sees repeats only until the first ask lands.
	comfyPokeEvery = 15 * time.Second
)

// comfySquat reports what unprotected ComfyUI runs hold right now, and
// which configured engines those are. An idle unloaded server stays under
// comfySquatMin and never blocks a grant.
func (b *Broker) comfySquat() (uint64, []ComfyEngine) {
	if len(b.cfg.Comfy) == 0 {
		return 0, nil
	}
	var n uint64
	var squat []ComfyEngine
	for _, e := range b.cfg.Comfy {
		for _, s := range b.squatters {
			if s.run == e.Name && s.proc.Used > 0 {
				n += s.proc.Used
				squat = append(squat, e)
				break
			}
		}
	}
	if n < comfySquatMin {
		return 0, nil
	}
	return n, squat
}

// comfyPoke asks a squatting ComfyUI to unload its models through its own
// /free. Rate limited per engine: evaluate runs every tick while a waiter
// is blocked, the engine hears about it at most once per comfyPokeEvery.
// The request runs in the background so a slow server cannot stall the
// scheduler. A failed ask is logged: the wait reason already says the ask
// went out, so silence would hide a wedged /free until the waiter times out.
func (b *Broker) comfyPoke(now time.Time, engines []ComfyEngine) {
	for _, e := range engines {
		if e.URL == "" {
			continue
		}
		if last, ok := b.comfyPoked[e.Name]; ok && now.Sub(last) < comfyPokeEvery {
			continue
		}
		b.comfyPoked[e.Name] = now
		b.log.Debug("asking ComfyUI to free its models", "engine", e.Name, "url", e.URL)
		go func(name, url string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := comfy.Free(ctx, url, true); err != nil {
				b.log.Warn("ComfyUI free ask failed; still waiting for the memory", "engine", name, "url", url, "err", err)
			}
		}(e.Name, e.URL)
	}
}
