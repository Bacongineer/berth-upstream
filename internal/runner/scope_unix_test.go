// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

import "testing"

func TestScopeAllowed(t *testing.T) {
	for _, tc := range []struct {
		notify string
		want   bool
	}{
		{"", true},
		{"/run/user/1000/systemd/notify", false},
		{"@/org/freedesktop/systemd1/notify/123", false},
	} {
		env := func(k string) string {
			if k == "NOTIFY_SOCKET" {
				return tc.notify
			}
			return ""
		}
		if got := scopeAllowed(env); got != tc.want {
			t.Errorf("NOTIFY_SOCKET=%q: scopeAllowed = %v, want %v", tc.notify, got, tc.want)
		}
	}
}
