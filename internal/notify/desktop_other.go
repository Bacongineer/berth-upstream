// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package notify

import (
	"errors"
	"runtime"
)

// Desktop has no notification service on this system yet.
func Desktop() (Sender, error) {
	return nil, errors.New("desktop notifications are not supported on " + runtime.GOOS + " yet")
}
