// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

// berth stop against a live daemon: the asked leases in the report, the
// exit codes of the contract (0 done, 1 unknown engine, 2 daemon down or a
// stuck lease under --wait), and the two --wait outcomes over the event
// log. The goroutines standing in for the engine's wrapper take the drain
// and hand the berth back, as the real wrapper does.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/node"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/server"
)

// startStopDaemon runs a berthd on a temp socket whose release grace the
// test sets, so --wait has both outcomes to find: a freed lease (the card
// shows it gone) and a stuck one (its process still holds after the grace).
func startStopDaemon(t *testing.T, grace time.Duration) (string, *broker.Broker, *procCard) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "berth.sock")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &procCard{}
	b := broker.New(broker.Config{
		Poll: 10 * time.Millisecond, DefaultWait: 5 * time.Second, JuggleWindow: -1,
		ReleaseGrace: grace, Version: "test", Log: log,
	}, c, procs.Map{101: 100})
	ln, err := server.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.Tick(ctx)
	go b.Run(ctx)
	go server.Serve(ctx, ln, node.One(b), server.Extras{}, log)
	return sock, b, c
}

// grantEngine leases the card to one engine with a run attached and a
// process on the card, the shape a real berth run leaves.
func grantEngine(t *testing.T, b *broker.Broker, c *procCard, engine string) *broker.Lease {
	t.Helper()
	l, err := b.Acquire(context.Background(), broker.Request{Engine: engine, Needs: 12 << 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: engine + "-serve", Used: 12 << 30})
	b.Tick(context.Background())
	return l
}

func TestStopAgainstADaemon(t *testing.T) {
	isolate(t)
	sock, b, c := startStopDaemon(t, time.Hour)
	l := grantEngine(t, b, c, "ninfer")
	go func() {
		if why := <-l.Drains(); why != "stopped by berth stop" {
			t.Errorf("drain reason = %q", why)
		}
		l.Release()
		c.set()
	}()
	code, out, errOut := runCLI(t, "stop", "ninfer", "--socket", sock)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "ninfer: 1 lease asked to drain: ninfer-1") {
		t.Errorf("stdout %q lacks the asked lease", out)
	}
}

func TestStopJSONIsTheDocument(t *testing.T) {
	isolate(t)
	sock, b, c := startStopDaemon(t, time.Hour)
	l := grantEngine(t, b, c, "ninfer")
	go func() {
		<-l.Drains()
		l.Release()
		c.set()
	}()
	code, out, _ := runCLI(t, "stop", "ninfer", "--socket", sock, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	var doc struct {
		Engine string   `json:"engine"`
		Leases []string `json:"leases"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if doc.Engine != "ninfer" || len(doc.Leases) != 1 || doc.Leases[0] != "ninfer-1" {
		t.Fatalf("doc = %+v, want engine ninfer with its one lease", doc)
	}
}

func TestStopUnknownEngine(t *testing.T) {
	isolate(t)
	sock, _, _ := startStopDaemon(t, time.Hour)
	code, out, errOut := runCLI(t, "stop", "nobody", "--socket", sock)
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d", code, ExitFail)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	if !strings.Contains(errOut, "no running engine named nobody under a lease") {
		t.Errorf("stderr = %q, want the refusal naming the engine", errOut)
	}
}

func TestStopUnreachable(t *testing.T) {
	isolate(t)
	missing := filepath.Join(t.TempDir(), "nope.sock")
	for _, args := range [][]string{
		{"stop", "ninfer", "--socket", missing},
		{"stop", "ninfer", "--socket", missing, "--format", "json"},
		{"stop", "ninfer", "--wait", "--socket", missing},
	} {
		code, out, errOut := runCLI(t, args...)
		if code != ExitSystem {
			t.Errorf("%v: exit = %d, want %d", args, code, ExitSystem)
		}
		if out != "" {
			t.Errorf("%v: stdout = %q, want empty", args, out)
		}
		if !strings.Contains(errOut, "[ERROR] berthd unreachable on "+missing) {
			t.Errorf("%v: stderr = %q", args, errOut)
		}
	}
	// A missing engine name is a usage error, decided before any daemon.
	if code, _, _ := runCLI(t, "stop", "--socket", missing); code != ExitFail {
		t.Errorf("no name: exit = %d, want %d", code, ExitFail)
	}
}

func TestStopWaitFollowsToFreed(t *testing.T) {
	isolate(t)
	sock, b, c := startStopDaemon(t, time.Hour)
	l := grantEngine(t, b, c, "ninfer")
	go func() {
		<-l.Drains()
		l.Release()
		c.set()
	}()
	code, out, errOut := runCLI(t, "stop", "ninfer", "--wait", "--socket", sock)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "freed: ninfer-1") {
		t.Errorf("stdout %q lacks the freed lease", out)
	}
}

// The process keeps holding after the release: past the grace the lease is
// stuck, --wait says so, and the exit code points the operator at reclaim.
func TestStopWaitStuckExitsSystem(t *testing.T) {
	isolate(t)
	sock, b, c := startStopDaemon(t, 100*time.Millisecond)
	l := grantEngine(t, b, c, "ninfer")
	go func() {
		<-l.Drains()
		l.Release()
	}()
	code, out, errOut := runCLI(t, "stop", "ninfer", "--wait", "--socket", sock)
	if code != ExitSystem {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitSystem, errOut)
	}
	if !strings.Contains(out, "stuck: ninfer-1") {
		t.Errorf("stdout %q lacks the stuck lease", out)
	}
	for _, want := range []string{"[ERROR] stuck: ninfer-1", "berth reclaim ninfer-1 --yes"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q lacks %q", errOut, want)
		}
	}
}
