// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"reflect"
	"testing"
)

func TestParseGPUs(t *testing.T) {
	for in, want := range map[string][]gpuSel{
		"all":                {},
		"1":                  {{index: 1}},
		"0,2":                {{index: 0}, {index: 2}},
		"1, 0000:02:00.0":    {{index: 1}, {addr: "0000:02:00.0"}},
		"00000000:41:00.0,3": {{addr: "0000:41:00.0"}, {index: 3}},
	} {
		got, err := parseGPUs(in, "auto")
		if err != nil || len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("parseGPUs(%q) = %+v, %v, want %+v", in, got, err, want)
		}
	}
	if got, _ := parseGPUs("all", "auto"); got != nil {
		t.Errorf("all = %+v, want nil", got)
	}
	for _, bad := range []string{"", "x", "1,", "-1", "02:00.0"} {
		if _, err := parseGPUs(bad, "auto"); err == nil {
			t.Errorf("parseGPUs(%q) took it", bad)
		}
	}
	if _, err := parseGPUs("0", "intel"); err == nil {
		t.Error("an unknown backend passed")
	}
}

// The first card keeps the state files a one-card berthd always wrote.
func TestCardFile(t *testing.T) {
	for _, c := range []struct {
		path string
		i    int
		name string
		want string
	}{
		{"/s/usage.json", 0, "gpu1", "/s/usage.json"},
		{"/s/usage.json", 1, "gpu1", "/s/usage.gpu1.json"},
		{"/s/counters.json", 2, "0000:02:00.0", "/s/counters.0000-02-00.0.json"},
		{"", 3, "gpu3", ""},
	} {
		if got := cardFile(c.path, c.i, c.name); got != c.want {
			t.Errorf("cardFile(%q, %d, %q) = %q, want %q", c.path, c.i, c.name, got, c.want)
		}
	}
}
