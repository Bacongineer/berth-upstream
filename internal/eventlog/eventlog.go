// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package eventlog is berthd's event stream: one numbered sequence for the
// whole machine, kept in a ring and fanned out to watchers. Every card's
// broker on a node writes to the same log, so `berth events` is one ordered
// stream however many cards the node governs.
package eventlog

import (
	"sync"

	"github.com/T-Crypt/berth/internal/proto"
)

// Ring is how many events the log keeps in memory.
const Ring = 1000

// watchBuffer is how far a watcher may fall behind before its channel is
// closed.
const watchBuffer = 256

// Log is one node's event stream.
type Log struct {
	mu     sync.Mutex
	seq    uint64
	events []proto.Event // ring, oldest first
	subs   map[chan proto.Event]struct{}
}

// New returns an empty log.
func New() *Log { return &Log{subs: map[chan proto.Event]struct{}{}} }

// Emit numbers e, keeps it, and hands it to every watcher. A watcher whose
// buffer is full is dropped, so one slow client never stalls a broker.
func (l *Log) Emit(e proto.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	e.Seq = l.seq
	if len(l.events) == Ring {
		copy(l.events, l.events[1:])
		l.events = l.events[:Ring-1]
	}
	l.events = append(l.events, e)
	for ch := range l.subs {
		select {
		case ch <- e:
		default:
			close(ch)
			delete(l.subs, ch)
		}
	}
}

// Last is the sequence number of the newest event, 0 before the first.
func (l *Log) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seq
}

// Events returns the events after since, and, if watch, a channel of every
// later one plus a func to stop watching. The channel closes if the watcher
// falls more than 256 events behind.
func (l *Log) Events(since uint64, watch bool) ([]proto.Event, <-chan proto.Event, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []proto.Event
	for _, e := range l.events {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	if !watch {
		return out, nil, func() {}
	}
	ch := make(chan proto.Event, watchBuffer)
	l.subs[ch] = struct{}{}
	return out, ch, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
	}
}
