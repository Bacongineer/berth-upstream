// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package config

import (
	"os"
	"syscall"
)

// Reject symlinks atomically; nonblocking open also makes a substituted FIFO safe.
func openCredential(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
