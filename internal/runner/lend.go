// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The runner's side of a lend. berthd decides when a serving engine should
// give part of its card to a waiter and when it may take that memory back;
// this calls the engine's own lend endpoints (internal/lend) and answers on
// the connection the lease rides. Nothing here grants or releases memory: the
// daemon waits for a card read, and an answer only ever reports what the
// engine said.

import (
	"context"
	"errors"

	"github.com/T-Crypt/berth/internal/httpauth"
	"github.com/T-Crypt/berth/internal/lend"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// lendCallTimeout is the window one lend or take-back call gets, the park
// call's: the engine answers when it has let the memory go, and a resize of an
// expert cache is not instant.
const lendCallTimeout = parkCallTimeout

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

// lendStep answers a lend ask: the engine's call, then OpLent with its ticket
// or OpLendFailed with the reason and its kind. The daemon confirms the memory
// with a card read; this answer only reports what the engine said.
func (l *leaseLine) lendStep(o Options, ask proto.Msg, say func(string, ...any)) {
	if o.Lend == nil {
		say("berthd asked %s for a lend, but the entry has no lend calls", o.Engine)
		_ = l.send(proto.Msg{Op: proto.OpLendFailed, Reason: o.Engine + " has no lend calls", Kind: "Unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), lendCallTimeout)
	t, err := o.Lend.Lend(httpauth.WithBearer(ctx, o.APIKey), lend.Ask{
		GPU:     ask.GPU,
		MiB:     ask.Needs / (1 << 20),
		FreeMiB: ask.Free,
	})
	cancel()
	if err != nil {
		say("lend of %s refused (%v); berthd falls back to a drain", server.MiB(ask.Needs), err)
		_ = l.send(proto.Msg{Op: proto.OpLendFailed, Reason: err.Error(), Kind: lendKind(err)})
		return
	}
	say("lent %s to a waiter (ticket %s)", server.MiB(t.MiB<<20), t.ID)
	if err := l.send(proto.Msg{Op: proto.OpLent, LendID: t.ID, Needs: t.MiB << 20}); err != nil {
		say("lent, but could not tell berthd (%v)", err)
	}
}

// returnStep answers a take-back ask: the engine grows back to the memory it
// had. A 409 says the lent memory is still in use, which is a reason to ask
// again later and nothing else; any other failure is reported without a retry,
// and the re-attach's ledger read is what reconciles the two.
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
	say("took back lend %s", id)
	if err := l.send(proto.Msg{Op: proto.OpReturned, LendID: id}); err != nil {
		say("took the lend back, but could not tell berthd (%v)", err)
	}
}
