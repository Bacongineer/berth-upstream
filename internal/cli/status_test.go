// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/T-Crypt/berth/internal/alerts"
	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// emptyCard is a machine whose card reports no processes, so the daemon below
// has nothing to report but its own state.
type emptyCard struct{}

func (emptyCard) Read(context.Context) (gpu.Snapshot, error) {
	return gpu.Snapshot{Total: 24 << 30, Free: 24 << 30, At: time.Now()}, nil
}

// startDaemon runs a real berthd on a temp socket: the CLI is then driven
// through the real wire, which is the only way to verify a document that has to
// match what the daemon sends.
func startDaemon(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "berth.sock")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := broker.New(broker.Config{
		Poll: 20 * time.Millisecond, DefaultWait: 5 * time.Second, JuggleWindow: -1,
		Version: "test", Log: log,
	}, emptyCard{}, procs.System{})
	ln, err := server.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// Read the card once before serving: Run's first read happens in its own
	// goroutine, and a status asked for before it shows no GPU at all.
	b.Tick(ctx)
	go b.Run(ctx)
	ev := alerts.New(alerts.FromConfig(config.AlertsConfig{}), 3, nil)
	lastAlerts := &atomic.Pointer[proto.Alerts]{}
	lastAlerts.Store(ev.Snapshot())
	go server.Serve(ctx, ln, b, server.Extras{Alerts: lastAlerts.Load}, log)
	return sock
}

func TestStatusHumanAgainstADaemon(t *testing.T) {
	isolate(t)
	sock := startDaemon(t)
	code, out, errOut := runCLI(t, "status", "--socket", sock)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "berthd test on "+sock) {
		t.Errorf("stdout %q lacks the daemon it read", out)
	}
	if !strings.Contains(out, "no leases, nobody waiting, no squatters") {
		t.Errorf("stdout %q lacks the empty state", out)
	}
}

func TestStatusJSONIsTheWireDocument(t *testing.T) {
	isolate(t)
	sock := startDaemon(t)
	code, out, _ := runCLI(t, "status", "--socket", sock, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	var st proto.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if st.API != proto.APIVersion {
		t.Errorf("api = %d, want %d", st.API, proto.APIVersion)
	}
	if len(st.GPUs) != 1 || st.GPUs[0].Scheduled != true {
		t.Errorf("gpus = %+v", st.GPUs)
	}
}

// procCard is a fake card whose processes the test sets, so a status can
// carry a footprint the way a real engine run leaves one.
type procCard struct {
	mu    sync.Mutex
	procs []gpu.Proc
}

func (c *procCard) set(ps ...gpu.Proc) {
	c.mu.Lock()
	c.procs = ps
	c.mu.Unlock()
}

func (c *procCard) Read(context.Context) (gpu.Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var used uint64
	for _, p := range c.procs {
		used += p.Used
	}
	return gpu.Snapshot{Total: 24 << 30, Used: used, Free: (24 << 30) - used, At: time.Now(),
		Procs: append([]gpu.Proc(nil), c.procs...)}, nil
}

// startFootprintDaemon is startDaemon with a card the test drives and one
// engine in the status, so the reply has a footprint to carry.
func startFootprintDaemon(t *testing.T) (string, *broker.Broker, *procCard) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "berth.sock")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &procCard{}
	b := broker.New(broker.Config{
		Poll: 20 * time.Millisecond, DefaultWait: 5 * time.Second, JuggleWindow: -1,
		Version: "test", Log: log,
	}, c, procs.Map{101: 100})
	ln, err := server.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.Tick(ctx)
	go b.Run(ctx)
	engines := func() []proto.Engine {
		return []proto.Engine{{Name: "llama", Type: "llamacpp", URL: "http://127.0.0.1:8080",
			Up: true, State: "up", Models: []proto.Model{{Name: "m", State: "loaded"}}}}
	}
	go server.Serve(ctx, ln, b, server.Extras{Engines: engines}, log)
	return sock, b, c
}

// The status reply carries the engine's footprint: the human view shows it
// as EST. VRAM, and the JSON names it estimated_vram_bytes.
func TestStatusCarriesTheEngineFootprint(t *testing.T) {
	isolate(t)
	sock, b, c := startFootprintDaemon(t)

	l, err := b.Acquire(context.Background(), broker.Request{Engine: "llama", Priority: broker.Foreground}, nil)
	if err != nil {
		t.Fatal(err)
	}
	l.Attach(100)
	c.set(gpu.Proc{PID: 101, Name: "llama-server", Used: 12 << 30})
	b.Tick(context.Background())
	l.Release()
	c.set()
	deadline := time.Now().Add(time.Second)
	for {
		if est, ok := b.Footprint("llama"); ok && est == 12<<30 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the freed run's footprint never folded")
		}
		time.Sleep(5 * time.Millisecond)
	}

	code, out, errOut := runCLI(t, "status", "--socket", sock)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "EST. VRAM") {
		t.Errorf("stdout %q lacks the EST. VRAM column", out)
	}
	if !strings.Contains(out, "12288 MiB") {
		t.Errorf("stdout %q lacks the engine's estimate", out)
	}

	code, out, _ = runCLI(t, "status", "--socket", sock, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	var st proto.Status
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if len(st.Footprints) != 1 || st.Footprints[0].Engine != "llama" || st.Footprints[0].Bytes != 12<<30 || st.Footprints[0].Runs != 1 {
		t.Errorf("footprints = %+v, want llama at 12 GiB over 1 run", st.Footprints)
	}
	if !strings.Contains(out, "estimated_vram_bytes") {
		t.Errorf("json %q lacks the estimated_vram_bytes field", out)
	}
}

func TestStatusYAML(t *testing.T) {
	isolate(t)
	sock := startDaemon(t)
	code, out, _ := runCLI(t, "status", "--socket", sock, "--format", "yaml")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	var st proto.Status
	if err := yaml.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("yaml %q: %v", out, err)
	}
	if st.API != proto.APIVersion {
		t.Errorf("api = %d, want %d", st.API, proto.APIVersion)
	}
}

// berthd unreachable is a system failure: exit 2, the reason on stderr, and
// nothing on stdout, in every format.
func TestStatusUnreachable(t *testing.T) {
	isolate(t)
	missing := filepath.Join(t.TempDir(), "nope.sock")
	for _, args := range [][]string{
		{"status", "--socket", missing},
		{"status", "--socket", missing, "--format", "json"},
		{"-q", "status", "--socket", missing},
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
}

// Without -f the backlog is printed and the command exits, so this terminates.
func TestEventsAgainstADaemon(t *testing.T) {
	isolate(t)
	sock := startDaemon(t)
	code, out, errOut := runCLI(t, "events", "--socket", sock)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, "  ") {
			t.Errorf("line %q is not an event line", line)
		}
	}
}

// --format json is one event per line, which is what a follower pipes into jq.
func TestEventsJSONIsLineDelimited(t *testing.T) {
	isolate(t)
	sock := startDaemon(t)
	code, out, errOut := runCLI(t, "events", "--socket", sock, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var e proto.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %q is not one event: %v", line, err)
		}
	}
}

func TestEventsBadSince(t *testing.T) {
	isolate(t)
	sock := startDaemon(t)
	code, _, errOut := runCLI(t, "events", "--socket", sock, "--since", "abc")
	if code != ExitSystem {
		t.Fatalf("exit = %d, want %d", code, ExitSystem)
	}
	if !strings.Contains(errOut, "invalid value") {
		t.Errorf("stderr = %q", errOut)
	}
}
