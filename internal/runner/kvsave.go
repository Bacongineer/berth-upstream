// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// The drain's KV snapshot step: an engine with kv_snapshots saves every
// idle slot's prompt cache into its snapshot directory while the drain
// waits to stop it, so the next start on the same model restores what would
// otherwise be prefilled again (see internal/kvsnap). The step is a rider,
// never a gate: whatever it hits, the drain goes on exactly as before.

package runner

import (
	"context"
	"errors"
	"time"

	"github.com/T-Crypt/berth/internal/httpauth"
	"github.com/T-Crypt/berth/internal/kvsnap"
)

// kvSaveTimeout bounds the whole step, saves and all, so a wedged engine
// holds the card no longer than this. A var so tests can shorten it.
var kvSaveTimeout = 60 * time.Second

// kvSave is that step of the drain, called after the slot boundary and
// before the park step or anything stops the engine. It saves the idle
// slots Plan hands back, records each save, then evicts the directory down
// to its budget. An engine without KVDir or KVBase skips everything.
func kvSave(o Options, done <-chan struct{}, say func(string, ...any)) {
	if o.KVDir == "" || o.KVBase == "" {
		return
	}
	say0 := say
	say = func(format string, a ...any) { say0("kv snapshots: "+format, a...) }
	ctx, cancel := context.WithTimeout(httpauth.WithBearer(context.Background(), o.APIKey), kvSaveTimeout)
	defer cancel()
	// An engine that exits mid-save ends the step at once; nothing left
	// to talk to.
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	idx, err := kvsnap.New(o.KVDir)
	if err != nil {
		say("the snapshot ledger did not open: %v", err)
		return
	}
	model, err := kvsnap.Model(ctx, o.KVBase)
	if err != nil {
		// No identity, no snapshot: an engine that lists no model yet, or
		// lists many (llama-swap), is skipped by design.
		say("skipped, %v", err)
		return
	}
	key := kvsnap.Key{Engine: o.Engine, Model: model}
	live, err := kvsnap.Slots(ctx, o.KVBase)
	if err != nil {
		say("the slot list did not read: %v", err)
		return
	}
	// The save of a slot writes about what its last save wrote; with no
	// earlier snapshot of that slot there is nothing to expect.
	prev := map[int]int64{}
	if ss, err := idx.For(key); err == nil {
		for _, s := range ss {
			prev[s.Slot] = s.Bytes
		}
	}
	saves := 0
	for _, s := range idx.Plan(key, live) {
		if ctx.Err() != nil {
			break // out of time, or the engine exited; said where it died
		}
		if under, warn := idx.Floor(uint64(prev[s.ID]), o.KVFloor); under {
			say("the snapshot directory is at its free-space floor; saving no more")
			break
		} else if warn != "" {
			say("%s", warn)
		}
		r, err := kvsnap.Save(ctx, o.KVBase, s.ID, kvsnap.Name(key, s.ID))
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				// The step outlived its budget, or the engine exited under it.
				if errors.Is(ctxErr, context.DeadlineExceeded) {
					say("gave up after %s with %d slot(s) saved", kvSaveTimeout, saves)
				}
				break
			}
			if errors.Is(err, kvsnap.ErrUnsupported) {
				say("%s was started without --slot-save-path; its slots stay unsaved", o.Engine)
				return
			}
			say("saving slot %d failed: %v", s.ID, err)
			continue
		}
		if err := idx.Record(key, s.ID, r, time.Now()); err != nil {
			say("recording slot %d failed: %v", s.ID, err)
			continue
		}
		saves++
	}
	if n, err := idx.Evict(o.KVBudget); err != nil {
		say("evicting to the budget failed: %v", err)
	} else if n > 0 {
		say("evicted %d snapshot(s) past the budget", n)
	}
}
