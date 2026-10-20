// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// A waiter's place in the queue and its start estimate. The head is next
// in line; each seat behind counts the waiters ahead and names the one
// directly ahead. The place is the queue's own order (priority, then the
// broker's arrival count), never a prediction: nobody behind a blocked
// waiter jumps it. The estimate prices only what the swap book has
// measured, one start per waiter ahead plus the waiter's own, from the
// tier each starts in: a resumed park is warm, everything else is cold.
// The measured sum is the number; an unmeasured start says "unmeasured"
// and adds none; the holder's drain is never in it, because a request
// runs as long as it runs.

import (
	"fmt"
	"time"

	"github.com/T-Crypt/berth/internal/swapcost"
)

// ordinal is a seat's number in English: 1st, 2nd, 3rd, 4th, with the
// teens as th no matter what the last digit says (11th, 12th, 13th).
func ordinal(n int) string {
	suffix := "th"
	switch n % 100 {
	case 11, 12, 13:
	default:
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return fmt.Sprintf("%d%s", n, suffix)
}

// placeLine is the opening of a wait reason: the head reads "next in
// line", each seat behind reads its ordinal and the lease directly ahead,
// by priority and engine.
func placeLine(seat int, ahead *lease) string {
	if ahead == nil {
		return "next in line: "
	}
	return fmt.Sprintf("%s in line (behind %s %s): ", ordinal(seat), ahead.prio.String(), ahead.engine)
}

// notifyWait tells every waiter where it stands: the head reads its place
// and the reason the queue is blocked, each seat behind reads its place,
// the lease directly ahead and the same reason, because the queue advances
// only when its head is granted. Where the swap book has measured the
// starts in a waiter's chain, the line ends with the start estimate.
// Called with b.mu held.
func (b *Broker) notifyWait(q []*lease, headWhy string) {
	for i, w := range q {
		var ahead *lease
		if i > 0 {
			ahead = q[i-1]
		}
		reason := placeLine(i+1, ahead) + headWhy
		if est := b.startEstimate(q[:i+1]); est != "" {
			reason += "; " + est
		}
		b.setWait(w, reason)
	}
}

// startEstimate is the estimate a waiting lease's reason ends with. chain
// is the lease's starts in order: every seat ahead, then its own. Empty
// when the broker has no book.
func (b *Broker) startEstimate(chain []*lease) string {
	book := b.cfg.SwapCost
	if book == nil {
		return ""
	}
	var total time.Duration
	unmeasured := 0
	for _, w := range chain {
		if es := book.Estimate(w.key(), b.startTier(w)); !es.Known {
			unmeasured++
		} else {
			total += es.Took
		}
	}
	switch {
	case unmeasured == 0:
		line := fmt.Sprintf("start about %d s once the card frees", int(total.Seconds()))
		if len(chain) > 1 {
			line += " (" + startsWord(len(chain)-1) + " ahead, measured)"
		} else {
			line += " (measured)"
		}
		return line
	case unmeasured == len(chain):
		return "its start is unmeasured"
	default:
		return fmt.Sprintf("start at least %d s once the card frees (%s ahead, %d unmeasured)",
			int(total.Seconds()), startsWord(len(chain)-1), unmeasured)
	}
}

// startsWord is a count of starts as the reader sees it: "1 start",
// "2 starts".
func startsWord(n int) string {
	if n == 1 {
		return "1 start"
	}
	return fmt.Sprintf("%d starts", n)
}

// startTier is the tier a waiting engine starts in: one that came back
// from a park holds its weights in RAM and resumes warm, everything else
// starts cold, because the broker cannot see the page cache.
func (b *Broker) startTier(w *lease) swapcost.Tier {
	if w.resumed {
		return swapcost.Warm
	}
	return swapcost.Cold
}
