// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package swapcost

import (
	"reflect"
	"testing"
	"time"
)

func TestPlanPrefetchAndKeepParked(t *testing.T) {
	b := emptyBook()
	chain := []Step{
		{Engine: "main", Tier: Hot},
		{Engine: "research", Tier: Cool},
		{Engine: "synthesize", Tier: Cold},
		{Engine: "main", Tier: Warm},
	}
	p := b.Plan(chain, testNow)

	wantPrefetch := []string{"research", "synthesize", "main", ""}
	if !reflect.DeepEqual(p.Prefetch, wantPrefetch) {
		t.Errorf("Prefetch = %v, want %v", p.Prefetch, wantPrefetch)
	}
	wantKeep := []string{"main"}
	if !reflect.DeepEqual(p.KeepParked, wantKeep) {
		t.Errorf("KeepParked = %v, want %v", p.KeepParked, wantKeep)
	}
	// The hot step is known at zero; the rest have no samples and stay
	// unknown, so the total is a lower bound of zero.
	if p.Unmeasured != len(chain)-1 || p.Total != 0 {
		t.Errorf("three steps unknown, total zero: unmeasured = %d, total = %s", p.Unmeasured, p.Total)
	}
	for i, step := range chain {
		got := p.Steps[i]
		if got.Step != step {
			t.Errorf("step %d = %+v, want %+v", i, got, step)
		}
		if step.Tier == Hot {
			if !got.Estimate.Known || got.Estimate.Took != 0 {
				t.Errorf("step %d hot is zero and known, got %+v", i, got.Estimate)
			}
			continue
		}
		if got.Estimate.Known || got.Estimate.Took != 0 {
			t.Errorf("step %d is unmeasured, got %+v", i, got.Estimate)
		}
	}
}

func TestPlanTotalAndUnmeasured(t *testing.T) {
	b := emptyBook()
	b.Record(sample("a", Cool, 600*time.Millisecond))
	b.Record(sample("a", Warm, 400*time.Millisecond))
	chain := []Step{
		{Engine: "a", Tier: Cool},
		{Engine: "b", Tier: Cold},
		{Engine: "a", Tier: Warm},
	}
	p := b.Plan(chain, testNow)

	if p.Total != time.Second {
		t.Errorf("the known steps sum to 1 s, got %s", p.Total)
	}
	if p.Unmeasured != 1 {
		t.Errorf("one step is unmeasured, got %d", p.Unmeasured)
	}
	wantPrefetch := []string{"b", "a", ""}
	if !reflect.DeepEqual(p.Prefetch, wantPrefetch) {
		t.Errorf("Prefetch = %v, want %v", p.Prefetch, wantPrefetch)
	}
	// a leaves the card for b and comes back, so it is kept parked; b
	// never returns.
	wantKeep := []string{"a"}
	if !reflect.DeepEqual(p.KeepParked, wantKeep) {
		t.Errorf("KeepParked = %v, want %v", p.KeepParked, wantKeep)
	}
	if p.Steps[1].Estimate.Known {
		t.Errorf("an unmeasured step stays unknown in the total, got %+v", p.Steps[1].Estimate)
	}
}

func TestPlanSameEngineConsecutive(t *testing.T) {
	b := emptyBook()
	b.Record(sample("a", Hot, 0))
	b.Record(sample("a", Warm, 400*time.Millisecond))
	chain := []Step{
		{Engine: "a", Tier: Hot},
		{Engine: "a", Tier: Warm},
		{Engine: "b", Tier: Cold},
	}
	p := b.Plan(chain, testNow)

	// Nothing to prefetch while a holds the card for both of its steps.
	wantPrefetch := []string{"", "b", ""}
	if !reflect.DeepEqual(p.Prefetch, wantPrefetch) {
		t.Errorf("Prefetch = %v, want %v", p.Prefetch, wantPrefetch)
	}
	// a is stopped for b and never comes back, so it is not kept parked.
	wantKeep := []string{}
	if !reflect.DeepEqual(p.KeepParked, wantKeep) {
		t.Errorf("KeepParked = %v, want %v", p.KeepParked, wantKeep)
	}
	// The hot step costs zero; only the warm step adds to the total.
	if p.Total != 400*time.Millisecond || p.Unmeasured != 1 {
		t.Errorf("total = %s, unmeasured = %d; want 400 ms and the cold step unmeasured", p.Total, p.Unmeasured)
	}
}
