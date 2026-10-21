// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"reflect"
	"testing"
)

// TestParseInstance reads PIDs and adapter LUIDs out of "GPU Process Memory"
// instance names. The luid_0x00000000_0x00015adb figures and PIDs are the
// real reading behind issue 82.
func TestParseInstance(t *testing.T) {
	governed := luid{hi: 0, lo: 0x00015adb}
	for _, tc := range []struct {
		in   string
		pid  int
		luid luid
		ok   bool
	}{
		{"pid_26152_luid_0x00000000_0x00015adb_phys_0", 26152, governed, true},
		{"pid_2164_luid_0x00000000_0x00015adb_phys_0", 2164, governed, true},
		{"pid_7_luid_0x00000000_0x00015adb_phys_1", 7, governed, true},
		{"pid_1234_luid_0x00000000_0x00003f9d_phys_0", 1234, luid{lo: 0x3f9d}, true},
		{"pid_8_luid_0x15adb_phys_0", 8, luid{}, true}, // one LUID part: malformed, PID still parses
		{"pid_9_phys_0", 9, luid{}, true},              // no LUID at all
		{"pid_0_phys_0", 0, luid{}, false},
		{"pid_x_luid_0x0_phys_0", 0, luid{}, false},
		{"pid__luid_0x0_phys_0", 0, luid{}, false},
		{"pid_", 0, luid{}, false},
		{"no pid", 0, luid{}, false},
		{"", 0, luid{}, false},
	} {
		pid, l, ok := parseInstance(tc.in)
		if pid != tc.pid || ok != tc.ok || l != tc.luid {
			t.Errorf("parseInstance(%q) = %d, %v, %v; want %d, %v, %v", tc.in, pid, l, ok, tc.pid, tc.luid, tc.ok)
		}
	}
}

// TestFilterByLuid keeps only the governed card's instances. The
// 0x0002abcd instances stand in for the other card on a two-GPU box; the
// governed card is the one from issue 82.
func TestFilterByLuid(t *testing.T) {
	governed := luid{lo: 0x00015adb}
	items := []dedicatedUsage{
		{Instance: "pid_26152_luid_0x00000000_0x00015adb_phys_0", Bytes: 103369371648},
		{Instance: "pid_2164_luid_0x00000000_0x00015adb_phys_0", Bytes: 2887462912},
		{Instance: "pid_35908_luid_0x00000000_0x0002abcd_phys_0", Bytes: 175656960},
		{Instance: "pid_44020_luid_0x00000000_0x0002abcd_phys_0", Bytes: 150003712},
		{Instance: "pid_99_luid_0x00000000_0x00015adb_phys_0", Bytes: 1},
	}
	kept := filterByLuid(items, map[luid]bool{governed: true})
	if len(kept) != 3 {
		t.Fatalf("kept %d instances, want 3: %+v", len(kept), kept)
	}
	for _, it := range kept {
		if _, l, _ := parseInstance(it.Instance); l != governed {
			t.Fatalf("instance %q is not on the governed card", it.Instance)
		}
	}
	// A nil want keeps every instance, as when the card could not be pinned.
	if got := filterByLuid(items, nil); len(got) != len(items) {
		t.Fatalf("nil want kept %d, want all %d", len(got), len(items))
	}
	// An empty (non-nil) set keeps nothing.
	if got := filterByLuid(items, map[luid]bool{}); len(got) != 0 {
		t.Fatalf("empty want kept %d, want 0", len(got))
	}
}

// TestDedicatedByPidSumsInstances sums a PID's instances; the LUID is
// ignored here, the filter has already run.
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

// TestCapToTotal bounds a PID's figure at the card's total. The 96.3 GiB
// reading and the 16303 MiB card are the real numbers from issue 82.
func TestCapToTotal(t *testing.T) {
	total := uint64(16303) * MiB
	dedicated := map[int]uint64{
		26152: 103369371648, // 96.3 GiB, six times the card
		2164:  2887462912,   // under the total, untouched
		25956: 241594368,
	}
	out, reads := capToTotal(dedicated, total)
	if out[26152] != total {
		t.Fatalf("26152 = %d, want the cap %d", out[26152], total)
	}
	if out[2164] != 2887462912 || out[25956] != 241594368 {
		t.Fatalf("uncapped figures moved: %+v", out)
	}
	if len(reads) != 1 || reads[0].pid != 26152 || reads[0].raw != 103369371648 || reads[0].total != total {
		t.Fatalf("reads = %+v, want one capped reading for 26152", reads)
	}
	// A zero total caps nothing.
	if out, reads = capToTotal(dedicated, 0); len(reads) != 0 || out[26152] != 103369371648 {
		t.Fatalf("total 0: out=%+v reads=%+v", out, reads)
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
		17: 70 << 20,  // a PDH-only PID the process table does not know
	}
	names := map[int]string{
		11: "not-nfer-serve", // nvidia-smi's own name wins for a listed PID
		13: "dwm.exe",        // a PDH-only PID takes the table's name
		16: "explorer.exe",
	}
	want := []Proc{
		{PID: 11, Name: "nfer-serve", Used: 10 << 20},
		{PID: 12, Name: "nvidia-smi", Used: 3 * MiB},
		{PID: 13, Name: "dwm.exe", Used: 100 << 20},
		{PID: 16, Name: "explorer.exe", Used: 65 << 20},
		{PID: 17, Used: 70 << 20}, // no table entry: the name stays empty
	}
	if got := mergeProcs(procs, dedicated, names); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeProcs = %+v, want %+v", got, want)
	}
	// A walk that found nothing (nil names) leaves every fresh PID nameless.
	nameless := []Proc{
		{PID: 11, Name: "nfer-serve", Used: 10 << 20},
		{PID: 12, Name: "nvidia-smi", Used: 3 * MiB},
		{PID: 13, Used: 100 << 20},
		{PID: 16, Used: 65 << 20},
		{PID: 17, Used: 70 << 20},
	}
	if got := mergeProcs(procs, dedicated, nil); !reflect.DeepEqual(got, nameless) {
		t.Fatalf("mergeProcs with no process table = %+v, want %+v", got, nameless)
	}
	if got := mergeProcs(procs, nil, names); !reflect.DeepEqual(got, procs) {
		t.Fatalf("mergeProcs with no PDH figures = %+v, want the list unchanged", got)
	}
}
