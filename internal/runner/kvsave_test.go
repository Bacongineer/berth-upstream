// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/kvsnap"
)

// llamaStub is a llama-server stand-in for the drain's snapshot step: one
// (or many, or no) model on /v1/models, an idle slot list, saves that
// answer what the test sets, and every slot call counted in request order.
type llamaStub struct {
	mu         sync.Mutex
	models     []string // what /v1/models lists, in order
	noSavePath bool     // started without --slot-save-path
	saveDelay  time.Duration
	saves      []int // slot ids POSTed to action=save, in order
	written    map[int]int
}

func (s *llamaStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			s.mu.Lock()
			models := append([]string(nil), s.models...)
			s.mu.Unlock()
			data := ""
			for _, m := range models {
				data += fmt.Sprintf(`{"id":%q},`, m)
			}
			fmt.Fprintf(w, `{"object":"list","data":[%s]}`, strings.TrimSuffix(data, ","))
		case r.URL.Path == "/slots" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[{"id":0,"is_processing":false},{"id":1,"is_processing":false}]`)
		case strings.HasPrefix(r.URL.Path, "/slots/"):
			slot := 0
			fmt.Sscanf(r.URL.Path, "/slots/%d", &slot)
			if r.URL.Query().Get("action") == "save" {
				s.mu.Lock()
				s.saves = append(s.saves, slot)
				s.mu.Unlock()
				if s.noSavePath { // an action without --slot-save-path
					fmt.Fprint(w, `{"error":{"code":501,"message":"This server does not support slots action. Start it with --slot-save-path","type":"not_supported_error"}}`)
					return
				}
				select {
				case <-time.After(s.saveDelay):
				case <-r.Context().Done(): // a client that gave up ends the save
				}
				fmt.Fprintf(w, `{"id_slot":%d,"n_saved":%d,"n_written":%d,"timings":{"save_ms":1.5}}`,
					slot, s.written[slot], 64*1024*1024)
				return
			}
			fmt.Fprint(w, `{"error":{"code":501,"message":"This server does not support slots action. Start it with --slot-save-path","type":"not_supported_error"}}`)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *llamaStub) saveCalls() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.saves...)
}

// ledger is what the snapshot directory's index records after a drain.
func ledger(t *testing.T, dir string) []kvsnap.Snapshot {
	t.Helper()
	x, err := kvsnap.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []kvsnap.Snapshot
	for _, m := range []string{"qwen3-8b"} {
		ss, err := x.For(kvsnap.Key{Engine: "llama-server", Model: m})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ss...)
	}
	return out
}

// The drain saves each idle slot, and only a save that wrote tokens is
// kept: an empty slot's file is worthless, so the ledger drops it.
func TestDrainKVSavesSlotsWithTokens(t *testing.T) {
	stub := &llamaStub{models: []string{"qwen3-8b"},
		written: map[int]int{0: 4000, 1: 0}} // slot 1's cache is empty
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	dir := t.TempDir()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	go drain(Options{Engine: "llama-server", DrainSlots: srv.URL, KVBase: srv.URL,
		KVDir: dir, KVBudget: 1 << 30, Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond},
		cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatalf("engine survived the drain; log:\n%s", &logs)
	}
	if got := stub.saveCalls(); len(got) != 2 {
		t.Fatalf("saves = %v, want both idle slots; log:\n%s", got, &logs)
	}
	l := ledger(t, dir)
	if len(l) != 1 || l[0].Slot != 0 || l[0].Tokens != 4000 {
		t.Fatalf("ledger = %+v, want slot 0 alone with its 4000 tokens", l)
	}
}

// An engine started without --slot-save-path gets one INFO line naming the
// flag, and the drain carries on and stops the engine exactly as before.
func TestDrainKVUnsupportedStops(t *testing.T) {
	stub := &llamaStub{models: []string{"qwen3-8b"}, noSavePath: true}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	dir := t.TempDir()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	drain(Options{Engine: "llama-server", DrainSlots: srv.URL, KVBase: srv.URL,
		KVDir: dir, Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond},
		cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine survived a drain whose saves were unsupported")
	}
	if got := stub.saveCalls(); len(got) != 1 {
		out := logs.String()
		t.Fatalf("an unsupported save must end the step at once: %v; log:\n%s", got, out)
	}
	if out := logs.String(); !strings.Contains(out, "kv snapshots: llama-server was started without --slot-save-path") {
		t.Fatalf("no unsupported line; log:\n%s", out)
	}
	if l := ledger(t, dir); len(l) != 0 {
		t.Fatalf("an unsupported engine recorded %+v", l)
	}
}

// A save slower than the step's whole budget loses the race, the step says
// so once, and the drain still stops the engine.
func TestDrainKVSlowSaveTimesOutAndStops(t *testing.T) {
	stub := &llamaStub{models: []string{"qwen3-8b"}, saveDelay: 5 * time.Second,
		written: map[int]int{0: 4000, 1: 4000}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	dir := t.TempDir()

	orig := kvSaveTimeout
	kvSaveTimeout = 150 * time.Millisecond
	t.Cleanup(func() { kvSaveTimeout = orig })

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	drain(Options{Engine: "llama-server", DrainSlots: srv.URL, KVBase: srv.URL,
		KVDir: dir, Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond},
		cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine survived a drain whose saves never finished")
	}
	if out := logs.String(); !strings.Contains(out, "gave up after 150ms") {
		t.Fatalf("no timeout line; log:\n%s", out)
	}
}

// An engine with no kv_snapshots is drained as always: the step is never
// entered, so no save is asked of it at all.
func TestDrainWithoutKVAsksNothing(t *testing.T) {
	stub := &llamaStub{models: []string{"qwen3-8b"}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	drain(Options{Engine: "llama-server", DrainSlots: srv.URL,
		Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond},
		cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine survived a plain drain")
	}
	if got := stub.saveCalls(); len(got) != 0 {
		t.Fatalf("an engine with no kv_snapshots saw saves %v", got)
	}
}

// llama-swap lists every model it can load, so an engine that names several
// has no snapshot identity and the step skips before asking for a save.
func TestDrainKVManyModelsSkips(t *testing.T) {
	stub := &llamaStub{models: []string{"a", "b", "c"}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	dir := t.TempDir()

	cmd, waited := startSleep(t)
	done := make(chan struct{})
	var logs strings.Builder

	drain(Options{Engine: "llama-swap", DrainPost: srv.URL + "/unload", KVBase: srv.URL,
		KVDir: dir, Grace: 50 * time.Millisecond, Retry: 20 * time.Millisecond},
		cmd, newLine(nil), done, "test", sayTo(&logs))

	select {
	case <-waited:
	case <-time.After(3 * time.Second):
		t.Fatal("engine survived the drain")
	}
	if got := stub.saveCalls(); len(got) != 0 {
		t.Fatalf("a many-model engine saw saves %v", got)
	}
	if out := logs.String(); !strings.Contains(out, "/v1/models lists 3 models") {
		t.Fatalf("no skip line; log:\n%s", out)
	}
}

// OptionsFor builds the step's inputs from the entry: the drain_slots base
// wins, then the drain_post origin, then the engine URL; the budget takes
// its default; entries without kv_snapshots carry nothing.
func TestOptionsForKVSnapshots(t *testing.T) {
	e := &config.Engine{Name: "llama-server", URL: "http://127.0.0.1:8092", KVSnapshots: "/tmp/kv",
		DrainPost: "http://127.0.0.1:8091/unload"}
	o := OptionsFor(e, Over{Socket: "s"})
	if o.KVBase != "http://127.0.0.1:8091" {
		t.Errorf("base = %q, want the drain_post origin", o.KVBase)
	}
	if o.KVBudget != config.DefaultKVSnapshotsMiB<<20 {
		t.Errorf("budget = %d, want the default", o.KVBudget)
	}
	e.DrainSlots, e.DrainPost, e.URL = "http://127.0.0.1:8090", "", ""
	o = OptionsFor(e, Over{Socket: "s"})
	if o.KVBase != "http://127.0.0.1:8090" {
		t.Errorf("base = %q, want the drain_slots base", o.KVBase)
	}
	e.DrainSlots = ""
	e.URL = "http://127.0.0.1:8092"
	o = OptionsFor(e, Over{Socket: "s"})
	if o.KVBase != "http://127.0.0.1:8092" {
		t.Errorf("base = %q, want the engine URL", o.KVBase)
	}
	if o.KVFloor != config.DefaultFreeFloor {
		t.Errorf("floor = %g, want the library default", o.KVFloor)
	}
	bare := OptionsFor(&config.Engine{Name: "x", URL: "http://127.0.0.1:8092"}, Over{Socket: "s"})
	if bare.KVDir != "" || bare.KVBudget != 0 || bare.KVBase != "" || bare.KVFloor != 0 {
		t.Errorf("bare = %+v, want the step off", bare)
	}
}
