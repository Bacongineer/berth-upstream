// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import "testing"

// unitName maps an engine name onto what a systemd unit name may carry,
// so a name like "qwen3.8/27b" cannot make systemd-run fail.
func TestUnitName(t *testing.T) {
	for _, c := range []struct {
		name, want string
	}{
		{"strata", "strata"},
		{"qwen3.8/27b", "qwen3.8_27b"},
		{"ninfer serve", "ninfer_serve"},
		{"safe.name_:weird", "safe.name_:weird"},
		{"сyrillic", "__yrillic"},
	} {
		if got := unitName(c.name); got != c.want {
			t.Errorf("unitName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	long := "a-really-long-engine-name-over-sixty-four-bytes-continues-with-more-words"
	if got := unitName(long); got != long[:64] {
		t.Errorf("unitName caps the name at 64 bytes: %q", got)
	}
	if got := unitName("x"); len(got) > 64 {
		t.Errorf("unitName length = %d, want at most 64", len(got))
	}
}
