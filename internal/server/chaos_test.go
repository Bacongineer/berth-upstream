// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

// Scenario 7 from contrib/chaos, against the fake GPU probe: the daemon
// restarts while an engine holds the card. The engine keeps running, is
// on record as unprotected while no daemon answers, and re-attaches to the
// next daemon; a request that arrives in between is granted nothing.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/runner"
	"github.com/T-Crypt/berth/internal/runs"
)

func TestChaosScenario7DaemonRestartMidLease(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "berth.sock")
	_, stop := daemon(t, sock, time.Hour)

	// a re-dials at its own pace, which leaves b a guaranteed turn while
	// a is still only on record.
	a, outA := launch(runner.Options{Socket: sock, Engine: "llamacpp", Priority: "foreground",
		Retry: 700 * time.Millisecond, Grace: 200 * time.Millisecond, Argv: []string{"sleep", "30"}})
	waitFor(t, "a granted", func() bool { return strings.Contains(outA.String(), "granted") })

	// berthd dies mid-lease; a keeps running, unprotected until it answers.
	stop()
	waitFor(t, "a on record", func() bool {
		return len(runs.List(runs.Dir(sock), procs.System{}.StartTime)) == 1
	})
	_, _ = daemon(t, sock, time.Hour)

	// b asks for the card while a is still due back: the re-attach window
	// holds new grants, so nothing is granted in between.
	b, outB := launch(runner.Options{Socket: sock, Engine: "comfyui", Priority: "foreground",
		Retry: 20 * time.Millisecond, Grace: 200 * time.Millisecond, Argv: []string{"true"}})
	waitFor(t, "b held by the restart window", func() bool {
		return strings.Contains(outB.String(), "waiting: berthd restarted")
	})

	waitFor(t, "a re-attached", func() bool { return strings.Contains(outA.String(), "re-attached") })
	if strings.Contains(outB.String(), "granted") {
		t.Fatalf("b was granted before the re-attach; b stderr:\n%s", outB.String())
	}

	// Then b takes the card the usual way: it drains a first.
	waitFor(t, "b granted", func() bool { return strings.Contains(outB.String(), "granted") })
	if !strings.Contains(outB.String(), "draining llamacpp") {
		t.Fatalf("b's stderr shows no drain of a:\n%s", outB.String())
	}
	if code := <-a; code != 128+15 {
		t.Fatalf("a exit = %d, want 143 (SIGTERM); stderr:\n%s", code, outA.String())
	}
	if code := <-b; code != 0 {
		t.Fatalf("b exit = %d; stderr:\n%s", code, outB.String())
	}
}
