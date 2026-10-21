// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import "testing"

func TestLastLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"one\ntwo\n", "two"},
		{"\x1b[32m[INFO]\x1b[0m Using pytorch attention\n", "[INFO] Using pytorch attention"},
		{"a\n\x1b[1;31merror:\x1b[0m out of memory", "error: out of memory"},
		{"progress 10%\rprogress 90%\n", "progress 90%"},
	} {
		if got := lastLine(tc.in); got != tc.want {
			t.Errorf("lastLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
