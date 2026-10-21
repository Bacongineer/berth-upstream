// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	s, err := Parse(0, "0, NVIDIA GeForce RTX 4090, 24564, 23134, 1030\n",
		"9947, /opt/ninfer/bin/ninfer-serve, 23106\n12, odd, name, [N/A]\n", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Index != 0 || s.Name != "NVIDIA GeForce RTX 4090" {
		t.Fatalf("device = %d %q", s.Index, s.Name)
	}
	if s.Total != 24564*MiB || s.Used != 23134*MiB || s.Free != 1030*MiB {
		t.Fatalf("memory = %d/%d/%d", s.Total, s.Used, s.Free)
	}
	if len(s.Procs) != 2 || s.Procs[0].PID != 9947 || s.Procs[0].Used != 23106*MiB {
		t.Fatalf("procs = %+v", s.Procs)
	}
	if s.Procs[1].Name != "odd, name" || s.Procs[1].Used != 0 {
		t.Fatalf("comma name / N/A = %+v", s.Procs[1])
	}
	if s.Baseline() != 28*MiB {
		t.Fatalf("baseline = %d MiB", s.Baseline()/MiB)
	}
}

func TestParseNoApps(t *testing.T) {
	s, err := Parse(1, "1, X, 24564, 500, 24064", "", time.Time{})
	if err != nil || len(s.Procs) != 0 || s.Baseline() != 500*MiB {
		t.Fatalf("s=%+v err=%v", s, err)
	}
}

// A 5080 + 4070 Ti box: both cards inventoried with their own limits,
// the snapshot is the scheduled one, and a missing index is an error.
func TestParseTwoCards(t *testing.T) {
	mem := "0, NVIDIA GeForce RTX 5080, 16303, 9000, 7303\n1, NVIDIA GeForce RTX 4070 Ti, 12282, 400, 11882\n"
	s, err := Parse(1, mem, "", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Devices) != 2 || s.Devices[0].Total != 16303*MiB || s.Devices[1].Total != 12282*MiB {
		t.Fatalf("devices = %+v", s.Devices)
	}
	if s.Index != 1 || s.Name != "NVIDIA GeForce RTX 4070 Ti" || s.Total != 12282*MiB {
		t.Fatalf("scheduled = %d %q %d", s.Index, s.Name, s.Total)
	}
	if _, err := Parse(2, mem, "", time.Time{}); err == nil {
		t.Fatal("index 2 does not exist and must be an error")
	}
}

func TestParseTelemetry(t *testing.T) {
	devs, err := ParseDevices("0, NVIDIA GeForce RTX 4090, 00000000:01:00.0, 24564, 23134, 1030, 97, 71, 402.55, 450.00, [N/A], 2730, 10501, 4, 16\n")
	if err != nil {
		t.Fatal(err)
	}
	d := devs[0]
	if d.BusID != "00000000:01:00.0" {
		t.Fatalf("bus id = %q", d.BusID)
	}
	if d.Util != 97 || d.Temp != 71 || d.Power != 402.55 || d.PowerLimit != 450 || d.Fan != -1 || d.ClockSM != 2730 || d.PCIeGen != 4 || d.PCIeWidth != 16 {
		t.Fatalf("telemetry = %+v (an [N/A] fan must read -1)", d)
	}
}

func TestBusID(t *testing.T) {
	s := Snapshot{
		Index: 1,
		Devices: []Device{
			{Index: 0, Name: "A", BusID: "00000000:01:00.0"},
			{Index: 1, Name: "B", BusID: "00000000:02:00.0"},
		},
	}
	if got := busID(s); got != "00000000:02:00.0" {
		t.Fatalf("busID = %q, want the governed card's id", got)
	}
	if got := busID(Snapshot{Index: 9}); got != "" {
		t.Fatalf("busID for a missing card = %q, want empty", got)
	}
}

func TestParseBusID(t *testing.T) {
	for _, tc := range []struct {
		in  string
		bus uint32
		dev uint32
		fn  uint32
		ok  bool
	}{
		{"00000000:01:00.0", 0x01, 0x00, 0x0, true},
		{"00000000:36:00.0", 0x36, 0x00, 0x0, true},
		{"00000001:ab:12.3", 0xab, 0x12, 0x3, true},
		{"0000:01:00.0", 0x01, 0x00, 0x0, true}, // short domain still parses
		{"00000000:01:00", 0, 0, 0, false},      // no function
		{"00000000:01", 0, 0, 0, false},         // no device
		{"01:00.0", 0, 0, 0, false},             // no domain separator
		{"", 0, 0, 0, false},
		{"00000000:zz:00.0", 0, 0, 0, false}, // bad hex
	} {
		bus, dev, fn, ok := parseBusID(tc.in)
		if bus != tc.bus || dev != tc.dev || fn != tc.fn || ok != tc.ok {
			t.Errorf("parseBusID(%q) = %d,%d,%d,%v; want %d,%d,%d,%v",
				tc.in, bus, dev, fn, ok, tc.bus, tc.dev, tc.fn, tc.ok)
		}
	}
}

// TestParseDevicesFieldCounts covers the three shapes nvidia-smi output takes:
// the full query, the query without its telemetry tail, and the memory fields
// only.
func TestParseDevicesFieldCounts(t *testing.T) {
	devs, err := ParseDevices("1, X, 00000000:02:00.0, 24564, 500, 24064\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].BusID != "00000000:02:00.0" || devs[0].Total != 24564*MiB || devs[0].Util != -1 {
		t.Fatalf("6-field = %+v", devs)
	}
	if _, err := ParseDevices("1, X, 24564, 500\n"); err == nil {
		t.Fatal("4 fields must be an error")
	}
	if _, err := ParseDevices("1, X, 00000000:02:00.0, 24564, 500, 24064, 50\n"); err == nil {
		t.Fatal("7 fields must be an error")
	}
}
