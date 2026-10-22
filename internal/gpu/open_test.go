// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import "testing"

func TestValidateSelection(t *testing.T) {
	for _, backend := range []string{"", "auto", "nvidia", "amd"} {
		if err := ValidateSelection(backend, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, opts := range []Options{{Backend: "cuda"}, {Backend: "amd", Index: -1}} {
		if _, err := Open(opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
	p, err := Open(Options{Backend: "nvidia"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(NvidiaSMI); !ok {
		t.Fatalf("one-shot NVIDIA probe changed: %T", p)
	}
}

func TestParseCard(t *testing.T) {
	for _, tc := range []struct {
		in    string
		index int
		addr  string
	}{
		{"0", 0, ""},
		{"2", 2, ""},
		{"0000:02:00.0", 0, "0000:02:00.0"},
		{"00000000:0A:00.0", 0, "0000:0a:00.0"},
	} {
		index, addr, err := ParseCard(tc.in)
		if err != nil || index != tc.index || addr != tc.addr {
			t.Fatalf("ParseCard(%q) = %d, %q, %v; want %d, %q", tc.in, index, addr, err, tc.index, tc.addr)
		}
	}
	for _, in := range []string{"02:00.0", "gpu1", ""} {
		if _, _, err := ParseCard(in); err == nil {
			t.Fatalf("ParseCard(%q) accepted", in)
		}
	}
	if _, err := Open(Options{Backend: "amd", Addr: "0000:02:00.0"}); err == nil {
		t.Fatal("AMD accepted a PCI address")
	}
}
