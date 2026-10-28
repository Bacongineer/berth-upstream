// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package gateway

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestDetachStartsNewProcessGroup(t *testing.T) {
	cmd := exec.Command("berth", "run")
	detach(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatalf("SysProcAttr = %+v, want CREATE_NEW_PROCESS_GROUP", cmd.SysProcAttr)
	}
}
