// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/kvsnap"
)

// fakeModel is what every fake engine here reports as its one model.
const fakeModel = "qwen3-8b"

// kvFixture names a snapshot the test plants in the ledger: key, slot and
// what the engine is asked to restore under it.
func snapFor(engine, model string, slot int) kvsnap.Snapshot {
	key := kvsnap.Key{Engine: engine, Model: model}
	at := time.Now()
	return kvsnap.Snapshot{Key: key, Slot: slot, File: kvsnap.Name(key, slot), Tokens: 1000, Bytes: 4096, SavedAt: at}
}

// plantSnapshot writes one snapshot file plus the ledger entry naming it.
func plantSnapshot(t *testing.T, dir string, ss ...kvsnap.Snapshot) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	x, err := kvsnap.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if err := os.WriteFile(filepath.Join(dir, s.File), []byte("snapshot bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := x.Record(s.Key, s.Slot, kvsnap.Result{Tokens: s.Tokens, Bytes: s.Bytes}, s.SavedAt); err != nil {
			t.Fatal(err)
		}
	}
}

// kvLines reads back what the fake engine appended, in arrival order.
func kvLines(t *testing.T, kvlog string) []string {
	t.Helper()
	b, err := os.ReadFile(kvlog)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// engineEntry is the configured engine a gateway test starts: the fake
// engine command, one served model, and whatever snapshot directory the
// test gives it (empty: no kv_snapshots).
func engineEntry(t *testing.T, addr, snapDir string) *config.Engine {
	t.Helper()
	return &config.Engine{Name: "llama-server", Type: "llamacpp", URL: "http://" + addr,
		Start: []string{os.Args[0]}, Serves: []string{fakeModel}, KVSnapshots: snapDir}
}

// A request for a model nobody runs names it through the handler: the
// gateway starts the engine, restores its saved slots, and proxies only
// then. The fake engine logs everything in arrival order, so the order is
// in the log itself.
func TestStartRestoresBeforeTheFirstRequest(t *testing.T) {
	sock, _ := daemon(t)
	addr := freeAddr(t)
	kvlog := filepath.Join(t.TempDir(), "calls.log")
	snapDir := filepath.Join(t.TempDir(), "kv")
	snap := snapFor("llama-server", fakeModel, 0)
	plantSnapshot(t, snapDir, snap)

	t.Setenv("BERTH_GW_ENGINE", addr+","+fakeModel+",300ms")
	t.Setenv("BERTH_GW_SLOTS", "1")
	t.Setenv("BERTH_GW_KVLOG", kvlog)

	e := engineEntry(t, addr, snapDir)
	g := &Gateway{Socket: sock, Ready: 15 * time.Second, Key: testKey, Log: discard(),
		Engines: []config.Engine{*e}}
	gw := httptest.NewServer(g.Handler())
	defer gw.Close()

	resp := call(t, "POST", gw.URL+"/v1/chat/completions",
		`{"model":"`+fakeModel+`","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	lines := kvLines(t, kvlog)
	restore, chat := -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "restore "):
			restore = i
			if !strings.HasSuffix(l, " "+snap.File) {
				t.Errorf("restored %q, want %q", l, snap.File)
			}
		case strings.HasPrefix(l, "chat "):
			chat = i
		}
	}
	if restore < 0 {
		t.Fatalf("no restore in %v", lines)
	}
	if chat < 0 {
		t.Fatalf("no chat request in %v", lines)
	}
	if restore > chat {
		t.Errorf("restore at %d, chat at %d: the first request landed on cold slots", restore, chat)
	}
}

// Only the engine's own model is restored: a snapshot of another model sits
// in the same directory without ever reaching the slots.
func TestStartRestoresOnlyTheEnginesModel(t *testing.T) {
	sock, _ := daemon(t)
	addr := freeAddr(t)
	kvlog := filepath.Join(t.TempDir(), "calls.log")
	snapDir := filepath.Join(t.TempDir(), "kv")
	own := snapFor("llama-server", fakeModel, 0)
	other := snapFor("llama-server", "the-other-model", 1)
	plantSnapshot(t, snapDir, other, own)

	t.Setenv("BERTH_GW_ENGINE", addr+","+fakeModel+",300ms")
	t.Setenv("BERTH_GW_SLOTS", "2")
	t.Setenv("BERTH_GW_KVLOG", kvlog)

	e := engineEntry(t, addr, snapDir)
	g := &Gateway{Socket: sock, Ready: 15 * time.Second, Key: testKey, Log: discard(),
		Engines: []config.Engine{*e}}
	if err := g.ensure(context.Background(), e, false); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	var got []string
	for _, l := range kvLines(t, kvlog) {
		if strings.HasPrefix(l, "restore ") {
			got = append(got, l)
		}
	}
	if len(got) != 1 || !strings.HasSuffix(got[0], " "+own.File) {
		t.Fatalf("restores = %v, want own.File alone", got)
	}
}

// An engine with no kv_snapshots never sees a slot call, save or restore.
func TestStartWithoutKVSnapshotsAsksNothing(t *testing.T) {
	sock, _ := daemon(t)
	addr := freeAddr(t)
	kvlog := filepath.Join(t.TempDir(), "calls.log")

	t.Setenv("BERTH_GW_ENGINE", addr+","+fakeModel+",300ms")
	t.Setenv("BERTH_GW_SLOTS", "1")
	t.Setenv("BERTH_GW_KVLOG", kvlog)

	e := engineEntry(t, addr, "")
	e.KVSnapshots = ""
	g := &Gateway{Socket: sock, Ready: 15 * time.Second, Key: testKey, Log: discard(),
		Engines: []config.Engine{*e}}
	if err := g.ensure(context.Background(), e, false); err != nil {
		t.Fatalf("start failed: %v", err)
	}
	for _, l := range kvLines(t, kvlog) {
		if strings.HasPrefix(l, "restore ") {
			t.Fatalf("an engine with no kv_snapshots saw %q", l)
		}
	}
}
