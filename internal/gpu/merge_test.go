// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"reflect"
	"testing"
)

func TestInstancePID(t *testing.T) {
	for _, tc := range []struct {
		in  string
		pid int
		ok  bool
	}{
		{"pid_1234_luid_0x00000000_0x00003f9d_phys_0", 1234, true},
		{"pid_7_luid_0x0_phys_1", 7, true},
		{"pid_0_phys_0", 0, false},
		{"pid_x_luid_0x0_phys_0", 0, false},
		{"pid__luid_0x0_phys_0", 0, false},
		{"pid_", 0, false},
		{"no pid", 0, false},
		{"", 0, false},
	} {
		if pid, ok := instancePID(tc.in); pid != tc.pid || ok != tc.ok {
			t.Errorf("instancePID(%q) = %d, %v", tc.in, pid, ok)
		}
	}
}

func TestDedicatedByPidSumsInstances(t *testing.T) {
	m := dedicatedByPid([]dedicatedUsage{
		{Instance: "pid_101_luid_0x0_phys_0", Bytes: 1 << 20},
		{Instance: "pid_101_luid_0x1_phys_0", Bytes: 2 << 20},
		{Instance: "pid_102_luid_0x0_phys_0", Bytes: 3 << 20},
		{Instance: "pid_103_luid_0x0_phys_0", Bytes: 0},
		{Instance: "system", Bytes: 9 << 20},
	})
	if m[101] != 3<<20 || m[102] != 3<<20 || len(m) != 2 {
		t.Fatalf("dedicated = %v", m)
	}
}

func TestMergeProcs(t *testing.T) {
	procs := []Proc{
		{PID: 11, Name: "nfer-serve", Used: 0}, // [N/A] under WDDM
		{PID: 12, Name: "nvidia-smi", Used: 3 * MiB},
	}
	dedicated := map[int]uint64{
		11: 10 << 20,  // fills the [N/A] compute app
		12: 99 << 20,  // nvidia-smi's own figure wins
		13: 100 << 20, // a graphics client over the floor joins
		14: 64 * MiB,  // exactly at the floor does not
		15: 63 * MiB,  // under it does not
		16: 65 << 20,  // another over the floor, after 13 by PID
	}
	want := []Proc{
		{PID: 11, Name: "nfer-serve", Used: 10 << 20},
		{PID: 12, Name: "nvidia-smi", Used: 3 * MiB},
		{PID: 13, Used: 100 << 20},
		{PID: 16, Used: 65 << 20},
	}
	if got := mergeProcs(procs, dedicated); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeProcs = %+v, want %+v", got, want)
	}
	if got := mergeProcs(procs, nil); !reflect.DeepEqual(got, procs) {
		t.Fatalf("mergeProcs with no PDH figures = %+v, want the list unchanged", got)
	}
}
