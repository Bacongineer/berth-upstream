// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
)

func readRecsFile(t *testing.T, path string) []engineRec {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs []engineRec
	if json.Unmarshal(b, &recs) != nil {
		t.Fatalf("state file = %s", b)
	}
	return recs
}

func TestGrantedLease(t *testing.T) {
	for _, c := range []struct {
		line, want string
		ok         bool
	}{
		{"berth: granted strata-14", "strata-14", true},
		{"berth: granted strata-14, WARNING: the card is contested", "strata-14", true},
		{"berth: waiting: the card is held", "", false},
		{"engine: cublasCreate: cuBLAS status 1", "", false},
	} {
		got, ok := grantedLease(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("grantedLease(%q) = %q %v, want %q %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// The record file carries the engine, its run process's PID and start
// time, and the lease the run was granted.
func TestEngineRecordsRoundTrip(t *testing.T) {
	state := filepath.Join(t.TempDir(), "engines.json")
	g := &Gateway{StateFile: state, Log: discard()}
	g.putRecord(engineRec{Engine: "strata", PID: 42, Start: 7, Started: time.Now()})
	g.setLease("strata", "strata-1")
	g.dropRecord("nope")
	recs := readRecsFile(t, state)
	if len(recs) != 1 || recs[0].Engine != "strata" || recs[0].PID != 42 || recs[0].Start != 7 || recs[0].Lease != "strata-1" {
		t.Fatalf("records = %+v", recs)
	}
	g.dropRecord("strata")
	if recs := readRecsFile(t, state); len(recs) != 0 {
		t.Fatalf("records after the drop = %+v", recs)
	}
}

// A gateway restart adopts the engines its predecessor started and that
// still run: the next request is proxied and no second start happens. A
// record whose process left is stale and dropped.
func TestRestartAdoptsItsEngines(t *testing.T) {
	sock, b := daemon(t)
	addr := freeAddr(t)
	t.Setenv("BERTH_GW_ENGINE", addr+",keeper,300ms")
	state := filepath.Join(t.TempDir(), "engines.json")
	engines := []config.Engine{{Name: "keeper", Type: "llamacpp", URL: "http://" + addr,
		Start: []string{os.Args[0]}, Serves: []string{"keeper"}}}
	mk := func() *Gateway {
		return &Gateway{Socket: sock, Ready: 10 * time.Second, Key: testKey, StateFile: state,
			Log: discard(), Engines: engines}
	}

	// The first gateway cold-starts the engine as its own run process.
	gw1 := httptest.NewServer(mk().Handler())
	resp := call(t, "POST", gw1.URL+"/v1/chat/completions", `{"model":"keeper"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "never collide") {
		t.Fatalf("first start: %d %s", resp.StatusCode, body)
	}
	gw1.Close() // "the gateway dies"; the engine process stays

	var recs []engineRec
	deadline := time.Now().Add(5 * time.Second)
	for {
		recs = readRecsFile(t, state)
		if len(recs) == 1 && recs[0].Lease != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the state file never recorded a granted run: %+v", recs)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A stale record beside the live one: a PID that belongs to no one.
	stale := recs[0]
	stale.Engine, stale.PID = "gone", stale.PID+100000
	b2, err := json.Marshal(append([]engineRec{stale}, recs...))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, b2, 0o600); err != nil {
		t.Fatal(err)
	}

	g2 := mk()
	gw2 := httptest.NewServer(g2.Handler())
	defer gw2.Close()
	if g2.running["keeper"] == nil || !g2.running["keeper"].answered {
		t.Fatal("the running engine was not adopted")
	}
	resp = call(t, "POST", gw2.URL+"/v1/chat/completions", `{"model":"keeper"}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "never collide") {
		t.Fatalf("after restart: %d %s", resp.StatusCode, body)
	}
	if n := len(b.Status().Leases); n != 1 {
		t.Fatalf("leases = %+v, want the adopted engine's one", b.Status().Leases)
	}
	after := readRecsFile(t, state)
	if len(after) != 1 || after[0].PID != recs[0].PID || after[0].Lease != recs[0].Lease {
		t.Fatalf("records after adoption = %+v, want only the live one", after)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
