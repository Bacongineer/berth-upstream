// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The runner's side of a lend. berthd decides when a serving engine should
// give part of its card to a waiter and when it may take that memory back;
// this calls the engine's own lend endpoints (internal/lend) and answers on
// the connection the lease rides. Nothing here grants or releases memory: the
// daemon waits for a card read, and an answer only ever reports what the
// engine said. The run also keeps its own ledger of the tickets it holds
// open: a re-attach names its one ticket so a restarted daemon counts the
// holder's budget less the lend, and a lend the engine answered 503 is
// watched for landing and handed straight back.

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/httpauth"
	"github.com/T-Crypt/berth/internal/lend"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// lendCallTimeout is the window one lend or take-back call gets. berthd reads
// the same constant, so it waits for the engine's answer as long as the run
// does.
const lendCallTimeout = lend.CallTimeout

// A 503 does not end the lend: it may still land. The run watches the engine's
// ledger for lendPendingWindow (the daemon's default release grace, the same
// window in which a lend may land), one read every lendPollEvery, and takes
// back at once any ticket the run does not hold. The pacing rides the lease
// line, set to these at its start; fields so a test can shorten the watch.
const (
	lendPendingWindow = 30 * time.Second
	lendPollEvery     = 5 * time.Second
)

// lendKind names a failed lend the way the broker's rule 4 reads it: TooBig,
// Busy, Pending or Unsupported. Anything else is the engine refusing for a
// reason only it knows.
func lendKind(err error) string {
	var tooBig *lend.TooBigError
	switch {
	case errors.As(err, &tooBig):
		return "TooBig"
	case errors.Is(err, lend.ErrPending):
		return "Pending"
	case errors.Is(err, lend.ErrBusy):
		return "Busy"
	case errors.Is(err, lend.ErrUnsupported):
		return "Unsupported"
	}
	return "Failed"
}

// record enters a ticket the engine granted to the run's ledger.
func (l *leaseLine) record(t lend.Ticket) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lends == nil {
		l.lends = make(map[string]lend.Ticket)
	}
	l.lends[t.ID] = t
}

// forget leaves a take-back the engine confirmed (its 404 counts) a ticket in;
// the run owes nothing for it, and a re-attach names no lend.
func (l *leaseLine) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.lends, id)
}

// knows is whether the run holds a ticket: its own ledger, not the engine's
// list, decides what a take-back is owed for.
func (l *leaseLine) knows(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.lends[id]
	return ok
}

// openLend is the ticket a re-attach names, when the run holds one open. At
// most one lend is open per holder, so the ledger holds one entry; sorted ids
// keep the answer deterministic if the engine ever lists more.
func (l *leaseLine) openLend() (lend.Ticket, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lends) == 0 {
		return lend.Ticket{}, false
	}
	ids := make([]string, 0, len(l.lends))
	for id := range l.lends {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return l.lends[ids[0]], true
}

// adoptLends rebuilds the ledger from the engine after the wrapper loses its
// own memory of the lend. Only an empty ledger is adopted: what this process
// lent it already knows. Strata lists nothing, which is right: its synthetic
// ticket lives and dies with the process that asked for it.
func (l *leaseLine) adoptLends(o Options, say func(string, ...any)) {
	if o.Lend == nil || o.Lend.Open == nil {
		return
	}
	l.mu.Lock()
	empty := len(l.lends) == 0
	l.mu.Unlock()
	if !empty {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lendCallTimeout)
	defer cancel()
	ts, err := o.Lend.Open(httpauth.WithBearer(ctx, o.APIKey))
	if err != nil {
		say("could not read %s's open lends (%v); berthd will count its full budget", o.Engine, err)
		return
	}
	for _, t := range ts {
		l.record(t)
		say("adopted %s's open lend (ticket %s, %s)", o.Engine, t.ID, server.MiB(t.MiB<<20))
	}
}

// reconcileLend watches a lend the engine did not answer (its 503) for as long
// as it may still land. Nothing was granted on that ask, so any ticket the
// run does not hold goes straight back, and a take-back of it overlaps no
// one. A ticket that refuses (still in use) is tried again on the next read.
func (l *leaseLine) reconcileLend(ctx context.Context, o Options, say func(string, ...any)) {
	for {
		select {
		case <-time.After(l.lendEvery):
		case <-ctx.Done():
			return
		}
		ts, err := o.Lend.Open(httpauth.WithBearer(ctx, o.APIKey))
		if err != nil {
			continue
		}
		for _, t := range ts {
			if ctx.Err() != nil {
				return
			}
			if l.knows(t.ID) {
				continue
			}
			say("a lend the engine did not answer landed (ticket %s, %s); nothing was granted on it, taking it back",
				t.ID, server.MiB(t.MiB<<20))
			rctx, rcancel := context.WithTimeout(ctx, lendCallTimeout)
			err := o.Lend.Return(httpauth.WithBearer(rctx, o.APIKey), t)
			rcancel()
			if err != nil {
				say("take-back of lend %s refused (%v); asking again on the next read", t.ID, err)
			}
		}
	}
}

// lendStep answers a lend ask: the engine's call, then OpLent with its ticket
// or OpLendFailed with the reason and its kind. The daemon confirms the memory
// with a card read; this answer only reports what the engine said. A ticket
// that lands enters the run's ledger; a 503 starts the watch for a lend that
// lands anyway.
func (l *leaseLine) lendStep(o Options, ask proto.Msg, say func(string, ...any)) {
	if o.Lend == nil {
		say("berthd asked %s for a lend, but the entry has no lend calls", o.Engine)
		_ = l.send(proto.Msg{Op: proto.OpLendFailed, Reason: o.Engine + " has no lend calls", Kind: "Unsupported"})
		return
	}
	l.mu.Lock()
	if l.endWatch != nil {
		l.endWatch()
		l.endWatch = nil
	}
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), lendCallTimeout)
	t, err := o.Lend.Lend(httpauth.WithBearer(ctx, o.APIKey), lend.Ask{
		GPU:     ask.GPU,
		MiB:     ask.Needs / (1 << 20),
		FreeMiB: ask.Free,
	})
	cancel()
	if err != nil {
		say("lend of %s refused (%v); berthd falls back to a drain", server.MiB(ask.Needs), err)
		// The watch is armed before berthd hears the refusal, so a lend it
		// asks next always finds the watch to end.
		var watch context.Context
		if errors.Is(err, lend.ErrPending) {
			var cancel context.CancelFunc
			watch, cancel = context.WithTimeout(context.Background(), l.lendWindow)
			defer cancel()
			l.mu.Lock()
			l.endWatch = cancel
			l.mu.Unlock()
		}
		if err := l.send(proto.Msg{Op: proto.OpLendFailed, Reason: err.Error(), Kind: lendKind(err)}); err != nil {
			say("could not tell berthd the lend failed (%v)", err)
		}
		if watch != nil {
			l.reconcileLend(watch, o, say)
		}
		return
	}
	l.record(t)
	say("lent %s to a waiter (ticket %s)", server.MiB(t.MiB<<20), t.ID)
	if err := l.send(proto.Msg{Op: proto.OpLent, LendID: t.ID, Needs: t.MiB << 20}); err != nil {
		say("lent, but could not tell berthd (%v)", err)
	}
}

// returnStep answers a take-back ask: the engine grows back to the memory it
// had. A 409 says the lent memory is still in use, which is a reason to ask
// again later and nothing else; any other failure is reported without a retry,
// and the re-attach's ledger read is what reconciles the two. A confirmed
// take-back leaves the ticket out of the run's ledger.
func (l *leaseLine) returnStep(o Options, id string, say func(string, ...any)) {
	if o.Lend == nil {
		say("berthd asked %s to take a lend back, but the entry has no lend calls", o.Engine)
		_ = l.send(proto.Msg{Op: proto.OpReturnFailed, Reason: o.Engine + " has no lend calls", LendID: id})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lendCallTimeout)
	err := o.Lend.Return(httpauth.WithBearer(ctx, o.APIKey), lend.Ticket{ID: id})
	cancel()
	if err != nil {
		retry := errors.Is(err, lend.ErrStillInUse)
		say("take-back of lend %s refused (%v), retry: %v", id, err, retry)
		_ = l.send(proto.Msg{Op: proto.OpReturnFailed, Reason: err.Error(), Retry: retry, LendID: id})
		return
	}
	l.forget(id)
	say("took back lend %s", id)
	if err := l.send(proto.Msg{Op: proto.OpReturned, LendID: id}); err != nil {
		say("took the lend back, but could not tell berthd (%v)", err)
	}
}
