// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/runner"
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
	g2.mu.Lock()
	adopted := g2.running["keeper"]
	g2.mu.Unlock()
	if adopted == nil || !adopted.answered {
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

// An adopted engine that is still loading is adopted too: the next request
// waits for it like any start in flight, and no second copy starts.
func TestAdoptsAStillLoadingEngine(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "no-daemon.sock")
	addr := freeAddr(t)
	go fakeEngine(addr, "loader", time.Second)

	// A live run process the old gateway recorded; the engine behind it
	// answers only in a second, so nothing answers yet.
	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill(); _ = proc.Wait() })

	state := filepath.Join(t.TempDir(), "engines.json")
	bit, err := json.Marshal([]engineRec{{Engine: "sleeper", PID: proc.Process.Pid,
		Start: procs.System{}.StartTime(proc.Process.Pid)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, bit, 0o600); err != nil {
		t.Fatal(err)
	}

	// Any second start of the engine would land here.
	marker := filepath.Join(t.TempDir(), "starts")
	g := &Gateway{Socket: sock, Ready: 10 * time.Second, Key: testKey, StateFile: state, Log: discard(),
		Engines: []config.Engine{{Name: "sleeper", URL: "http://" + addr,
			Start: []string{"sh", "-c", `echo x >> "$1"`, "sh", marker}, Serves: []string{"loader"}}}}
	gw := httptest.NewServer(g.Handler())
	defer gw.Close()

	start := time.Now()
	resp := call(t, "POST", gw.URL+"/v1/chat/completions", `{"model":"loader"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "never collide") {
		t.Fatalf("request = %d %s", resp.StatusCode, body)
	}
	if time.Since(start) < 800*time.Millisecond {
		t.Fatalf("the request never waited for the loading engine: %s", time.Since(start))
	}
	if n, err := os.ReadFile(marker); err == nil && len(n) > 0 {
		t.Fatalf("the gateway started a second copy of a loading engine: %q", n)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// An engine run writes its own log file, not a pipe the gateway holds:
// when the gateway goes away the run keeps writing (its fd is the file),
// and the next gateway follows the log's new lines into its tail.
func TestEngineLogSurvivesTheGateway(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "no-daemon.sock")
	addr := freeAddr(t)
	t.Setenv("BERTH_GW_ENGINE", addr+",loggy,300ms")
	t.Setenv("BERTH_GW_LOG_EVERY", "50ms")
	state := filepath.Join(t.TempDir(), "engines.json")
	e := config.Engine{Name: "loggy", URL: "http://" + addr, Start: []string{os.Args[0]}}

	g1 := &Gateway{StateFile: state, Log: discard()}
	s := &started{exited: make(chan struct{}), stderr: &lockedBuf{}, output: &tailBuf{}}
	go func() {
		_, _ = g1.spawnRun(e.Name, s, runner.OptionsFor(&e, runner.Over{Socket: sock, Group: e.Name, Wait: 10 * time.Second}))
	}()

	logPath, err := g1.engineLogPath(e.Name)
	if err != nil {
		t.Fatal(err)
	}
	size := func() int64 {
		st, err := os.Stat(logPath)
		if err != nil {
			return 0
		}
		return st.Size()
	}
	deadline := time.Now().Add(10 * time.Second)
	for size() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the run's log never got its first line")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The gateway goes away: g1 and its follower are dropped. The run and
	// its engine are still alive and keep writing for at least a second.
	before := size()
	time.Sleep(time.Second)
	if after := size(); after <= before {
		t.Fatalf("the engine stopped writing after the gateway went away: %d -> %d bytes", before, after)
	}

	// A new gateway on the same state file adopts the engine, and its own
	// follower shows the log's new lines in the adopted tail.
	g2 := &Gateway{StateFile: state, Log: discard(), Engines: []config.Engine{e}}
	g2.Handler()
	g2.mu.Lock()
	adopted := g2.running[e.Name]
	g2.mu.Unlock()
	if adopted == nil || !adopted.answered {
		t.Fatal("the running engine was not adopted")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		adopted.output.mu.Lock()
		tail := string(adopted.output.b)
		adopted.output.mu.Unlock()
		if strings.Contains(tail, "engine line") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the adopted engine's tail never saw new lines: %q", tail)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A long-running engine's log starts over past its LogMax instead of filling
// the disk, and lines written after the rotation still reach the tail.
func TestEngineLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.log")
	w, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	g := &Gateway{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LogMax: 1 << 10}
	s := &started{exited: make(chan struct{}), stderr: &lockedBuf{}, output: &tailBuf{}}
	stop, done := make(chan struct{}), make(chan struct{})
	go g.followEngine("e", path, 0, stop, done, s)
	line := strings.Repeat("x", 99) + "\n"
	for i := 0; i < 40; i++ { // 4000 bytes, well past its LogMax
		if _, err := w.WriteString(line); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := w.WriteString("after the rotation\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * logEvery)
	close(stop)
	<-done
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() >= 4000 {
		t.Fatalf("log is %d bytes; it never started over", st.Size())
	}
	if !strings.Contains(s.output.String(), "after the rotation") {
		t.Fatal("a line written after the rotation never reached the tail")
	}
}
