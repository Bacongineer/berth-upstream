// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package sysd

// UnitName keeps a unit name one systemd-run will take, so an engine
// name like "qwen3.8/27b" cannot fail the start.

import "testing"

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
		if got := UnitName(c.name); got != c.want {
			t.Errorf("UnitName(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	long := "a-really-long-engine-name-over-sixty-four-bytes-continues-with-more-words"
	if got := UnitName(long); got != long[:64] {
		t.Errorf("UnitName caps the name at 64 bytes: %q", got)
	}
	if got := UnitName("x"); len(got) > 64 {
		t.Errorf("UnitName length = %d, want at most 64", len(got))
	}
}
