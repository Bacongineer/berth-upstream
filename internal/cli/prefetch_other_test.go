// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package cli

import (
	"strings"
	"testing"
)

// Off Linux the kernel has no page-cache interface, so the ask is refused
// rather than pretending the files were asked for.
func TestPrefetchOffLinuxRefuses(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, _, errOut := runCLI(t, "prefetch", "Qwen3-8B-Q4_K_M", "--dir", dir)
	if code != ExitSystem {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitSystem, errOut)
	}
	if !strings.Contains(errOut, "page-cache interface") {
		t.Errorf("the refusal does not name the missing interface: %s", errOut)
	}
}
