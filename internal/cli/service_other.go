// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package cli

import (
	"context"
	"os"
)

// Berth runs Linux and Windows: the user services are systemd user units
// on Linux and Task Scheduler logon tasks on Windows, and there is no
// third platform this command manages.
const unsupportedService = "berth service manages services on Linux (systemd user units) and Windows (Task Scheduler logon tasks); this platform has neither"

func liveUnitDir() (string, error) { return os.TempDir(), nil }

func serviceInstall(ctx context.Context, ops serviceOps, units []Unit, dryRun bool) (*serviceDoc, *Error) {
	return nil, Systemf(unsupportedService)
}

func serviceUninstall(ctx context.Context, ops serviceOps, units []Unit, dryRun bool) (*serviceDoc, *Error) {
	return nil, Systemf(unsupportedService)
}

func serviceStatus(ctx context.Context, ops serviceOps, units []Unit) (*serviceDoc, *Error) {
	return nil, Systemf(unsupportedService)
}
