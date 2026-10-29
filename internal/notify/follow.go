// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

// Source opens berthd's event stream: the backlog, then every new event,
// until the stream breaks and the channel closes.
type Source func(ctx context.Context) (<-chan proto.Event, error)

// Sender shows a note on the desktop.
type Sender interface {
	Send(Note) error
}

// Follow shows each event the level lets through, from start on, and
// opens the stream again retry after it breaks, until ctx ends. A
// restarted berthd numbers its events from one again, so the time tells
// them apart: an event no newer than the last one seen was already seen.
// cards says how many cards berthd schedules, asked once per stream; with
// more than one, each note names its card. A nil cards, or an answer of
// 0 when berthd does not say, means one.
func Follow(ctx context.Context, src Source, cards func() int, s Sender, level Level, start time.Time, retry time.Duration, log *slog.Logger) {
	seen := start
	gate := Gate{Window: 5 * time.Second}
	for ctx.Err() == nil {
		events, err := src(ctx)
		if err != nil {
			log.Debug("berthd unreachable", "err", err)
		}
		named := err == nil && cards != nil && cards() > 1
		for e := range events {
			if !e.At.After(seen) {
				continue
			}
			seen = e.At
			n, ok := For(e, level, named)
			if !ok || !gate.Let(n, e.At) {
				continue
			}
			if err := s.Send(n); err != nil {
				log.Warn("notification not shown", "kind", n.Kind, "err", err)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}
