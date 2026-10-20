// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package gateway

import "os/exec"

// detach has nothing to do here: the run process stays a plain child, the
// job object tying it to its own engine already limits what a gateway
// restart leaves behind.
func detach(*exec.Cmd) {}

// scopeArgv has no user manager to answer to on this platform.
func scopeArgv(name string, argv []string) []string { return argv }
