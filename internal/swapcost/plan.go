// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package swapcost

import (
	"time"
)

// Step is one engine in a chain an orchestrator declares ahead of time,
// with the tier it expects to find the engine in: hot if it still holds
// the card, warm if it is parked, cool or cold from the page cache's
// share of its files.
type Step struct {
	Engine string
	Tier   Tier
}

// StepCost is one step of a plan: the step and the estimate for the
// switch into it.
type StepCost struct {
	Step     Step
	Estimate Estimate
}

// Plan is a chain's switch cost in the book's numbers.
//
// Total sums the known steps only, and Unmeasured counts the rest, so
// Total is a lower bound, "at least Total, Unmeasured steps unmeasured",
// never a promise. Prefetch runs with Steps, one entry per step: it is the
// engine to warm while that step holds the card, empty when the next step
// is the same engine or the chain ends there. KeepParked lists the
// engines the chain stops and then comes back to, in the order it stops
// them, so the caller parks them in RAM instead of stopping them.
type Plan struct {
	Steps      []StepCost
	Total      time.Duration
	Unmeasured int
	Prefetch   []string
	KeepParked []string
}

// Plan works out a declared chain against the book. now is the time the
// plan is made; the book's estimates do not use it, so the answer holds
// for the whole time the chain runs at its declared tiers. The first step
// is the switch from whatever holds the card now, priced from the tier
// the chain declares for that engine.
func (b *Book) Plan(chain []Step, now time.Time) Plan {
	p := Plan{
		Steps:    make([]StepCost, len(chain)),
		Prefetch: make([]string, len(chain)),
	}
	for i, step := range chain {
		cost := b.Estimate(step.Engine, step.Tier)
		p.Steps[i] = StepCost{Step: step, Estimate: cost}
		if cost.Known {
			p.Total += cost.Took
		} else {
			p.Unmeasured++
		}
		if i+1 < len(chain) && chain[i+1].Engine != step.Engine {
			p.Prefetch[i] = chain[i+1].Engine
		}
	}
	p.KeepParked = keepParked(chain)
	return p
}

// keepParked lists the engines the chain stops and then comes back to,
// the ones worth parking in RAM at their stop, in the order the chain
// stops them.
func keepParked(chain []Step) []string {
	kept := []string{}
	stopped := map[string]bool{}
	for i := 0; i+1 < len(chain); i++ {
		if chain[i+1].Engine == chain[i].Engine {
			continue
		}
		e := chain[i].Engine
		if stopped[e] {
			continue
		}
		stopped[e] = true
		for j := i + 1; j < len(chain); j++ {
			if chain[j].Engine == e {
				kept = append(kept, e)
				break
			}
		}
	}
	return kept
}
