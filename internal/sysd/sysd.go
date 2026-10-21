// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package sysd starts a command inside a transient scope of the systemd
// user manager, so it leaves the cgroup of whoever started it. Berth
// scopes every engine process it starts (the gateway's runs, and a
// `berth run` wrapper's own engine), and a scope's cgroup names those
// processes for attribution even when they escape the process tree.
package sysd

// UnitName maps an engine name onto what a systemd unit name may carry
// (ASCII letters, digits, and . - _ :), replacing every other byte with
// _ and capping the length so a long name still leaves room for the
// suffix. Without this, a name like "qwen3.8/27b" makes systemd-run fail
// and the start fail for the oddest reason.
func UnitName(name string) string {
	const max = 64
	b := []byte(name)
	if len(b) > max {
		b = b[:max]
	}
	for i, c := range b {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-', c == ':':
		default:
			b[i] = '_'
		}
	}
	return string(b)
}
