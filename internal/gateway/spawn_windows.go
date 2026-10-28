// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package gateway

import (
	"os/exec"
	"syscall"
)

// detach starts the run in its own console process group, so a Ctrl-C in the
// console running the gateway does not reach it and drain its engine. The
// run stays a child of the gateway; the job object tying it to its own
// engine limits what a gateway restart leaves behind.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
