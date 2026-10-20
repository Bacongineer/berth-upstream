// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// A waiter's place in the queue. The head is next in line; each seat behind
// counts the waiters ahead and names the one directly ahead. The place is
// the queue's own order (priority, then the broker's arrival count), never
// a prediction: nobody behind a blocked waiter jumps it.

import "fmt"

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
// only when its head is granted. Called with b.mu held.
func (b *Broker) notifyWait(q []*lease, headWhy string) {
	for i, w := range q {
		var ahead *lease
		if i > 0 {
			ahead = q[i-1]
		}
		b.setWait(w, placeLine(i+1, ahead)+headWhy)
	}
}
