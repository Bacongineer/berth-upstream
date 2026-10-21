// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package gpu

import (
	"testing"
	"unsafe"
)

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

// The D3DKMT structures must match d3dkmthk.h byte for byte on amd64: a
// D3DKMT_HANDLE is a 32-bit UINT, not a pointer.
func TestD3DKMTLayout(t *testing.T) {
	var q d3dkmtQueryAdapterInfo
	for _, tc := range []struct {
		name      string
		got, want uintptr
	}{
		{"sizeof D3DKMT_ADAPTERINFO", unsafe.Sizeof(d3dkmtAdapterInfo{}), 20},
		{"sizeof D3DKMT_ENUMADAPTERS2", unsafe.Sizeof(d3dkmtEnumAdapters2{}), 16},
		{"sizeof D3DKMT_QUERYADAPTERINFO", unsafe.Sizeof(q), 24},
		{"offsetof pPrivateDriverData", unsafe.Offsetof(q.data), 8},
		{"offsetof PrivateDriverDataSize", unsafe.Offsetof(q.dataSize), 16},
		{"sizeof D3DKMT_ADAPTERADDRESS", unsafe.Sizeof(d3dkmtAdapterAddress{}), 12},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}
