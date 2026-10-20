// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package gpu

import "testing"

// The query opens and collects once on whatever machine the test finds.
// CI runners have no GPU, so a machine where the counter object is absent
// skips; a machine with one must open, collect and format cleanly, with
// zero or more instances. The values themselves are Task Manager's to
// decide, so none is asserted.
func TestGPUProcQueryOpensAndCollects(t *testing.T) {
	g, err := newGPUProcQuery()
	if err != nil {
		t.Skipf("the GPU Process Memory counter is unavailable here: %v", err)
	}
	defer g.close()
	if err := g.collect(); err != nil {
		t.Fatalf("collect: %v", err)
	}
	if found, err := g.dedicated(); err != nil {
		t.Fatalf("format: %v", err)
	} else {
		t.Logf("%d instances", len(found))
	}
}
