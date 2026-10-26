// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The runner answers a lend ask with the engine's own call: the entry's
// lend_url on the generic protocol, the built-in endpoints for a kind that has
// them, the run's credential on every one, and the engine's answer back to
// berthd unchanged. These drive one step against an httptest engine, with a
// pipe standing in for the daemon's connection.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
)

// lendStub is an engine's lend endpoints: it records exactly what came in and
// answers with what the test hands it.
type lendStub struct {
	mu      sync.Mutex
	path    string
	auth    string
	body    string
	status  int
	payload string
}

func (s *lendStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.path, s.auth, s.body = r.URL.Path, r.Header.Get("Authorization"), string(raw)
		status, payload := s.status, s.payload
		s.mu.Unlock()
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			fmt.Fprint(w, payload)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, payload)
	})
}

// saw is what the last request looked like.
func (s *lendStub) saw() (path, auth, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.auth, s.body
}

// lendEntry is a run of a vLLM-shaped engine with the generic lend endpoints,
// as its berth.toml entry and the daemon's ask would give them.
func lendEntry(url string) Options {
	o := OptionsFor(&config.Engine{Type: "vllm", Name: "maya", URL: url,
		Lend: "hook", LendURL: url}, Over{})
	o.APIKey = "sk-test"
	return o
}

// askOn stands a pipe in for the daemon's connection, hands one ask to the
// runner's step, and returns the answer the daemon end reads.
func askOn(t *testing.T, o Options, ask proto.Msg) proto.Msg {
	t.Helper()
	say := func(format string, args ...any) { t.Logf(format, args...) }
	runnerEnd, daemonEnd := net.Pipe()
	t.Cleanup(func() {
		_ = runnerEnd.Close()
		_ = daemonEnd.Close()
	})
	line := newLine(proto.NewConn(runnerEnd))
	daemon := proto.NewConn(daemonEnd)
	reply := make(chan proto.Msg, 1)
	go func() {
		m, err := daemon.Recv()
		if err == nil {
			reply <- m
		}
	}()
	switch ask.Op {
	case proto.OpLend:
		go line.lendStep(o, ask, say)
	case proto.OpReturn:
		go line.returnStep(o, ask.LendID, say)
	}
	select {
	case m := <-reply:
		return m
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: the runner never answered", ask.Op)
	}
	return proto.Msg{}
}

// A lend ask on an entry with lend_url reaches the engine's lend endpoint with
// run's key and the shortfall, and the ticket comes back.
func TestLendAskReachesTheEngineAndAnswersWithItsTicket(t *testing.T) {
	stub := &lendStub{payload: `{"id":"17","miB":4096,"gpu":"0000:09:00.0"}`}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	got := askOn(t, lendEntry(srv.URL), proto.Msg{Op: proto.OpLend, Needs: 4 << 30, Free: 2048, GPU: "0000:09:00.0"})

	if got.Op != proto.OpLent || got.LendID != "17" || got.Needs != 4<<30 {
		t.Fatalf("the runner answered op=%s ticket=%q needs=%d, want op=lent ticket=17 needs=%d",
			got.Op, got.LendID, got.Needs, int64(4)<<30)
	}
	path, auth, body := stub.saw()
	if path != "/vram/lend" {
		t.Fatalf("the engine was called at %s, want /vram/lend", path)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("the engine saw %q, want the run's bearer key", auth)
	}
	if body != `{"gpu":"0000:09:00.0","mib":4096}` {
		t.Fatalf("the engine was asked %s", body)
	}
}

// A strata entry needs no lend_url: the ask becomes one reserve_vram call that
// carries the whole request (free plus the shortfall), and the synthetic ticket
// comes back.
func TestLendAskOnStrataReservesTheWholeRequest(t *testing.T) {
	stub := &lendStub{payload: `{"vram_free_mib":24576}`}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	o := OptionsFor(&config.Engine{Type: "strata", Name: "strata", URL: srv.URL, Lend: "hook"}, Over{})
	o.APIKey = "sk-test"
	got := askOn(t, o, proto.Msg{Op: proto.OpLend, Needs: 4 << 30, Free: 20 << 10})

	if got.Op != proto.OpLent || got.LendID != "reserve" || got.Needs != 4<<30 {
		t.Fatalf("the runner answered op=%s ticket=%q needs=%d, want op=lent ticket=reserve needs=%d",
			got.Op, got.LendID, got.Needs, int64(4)<<30)
	}
	path, auth, body := stub.saw()
	if path != "/v1/vram" {
		t.Fatalf("the engine was called at %s, want /v1/vram", path)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("the engine saw %q, want the run's bearer key", auth)
	}
	if body != `{"reserve_mib":24576}` {
		t.Fatalf("the engine was asked %s", body)
	}
}

// A take-back the engine refuses with a 409 is reported with a retry, so
// berthd keeps the lend counted and asks again later.
func TestTakeBackRefusedReportsARetry(t *testing.T) {
	stub := &lendStub{status: http.StatusConflict, payload: `{"error":{"message":"the lent memory is still in use"}}`}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	got := askOn(t, lendEntry(srv.URL), proto.Msg{Op: proto.OpReturn, LendID: "17"})

	if got.Op != proto.OpReturnFailed || !got.Retry || got.LendID != "17" {
		t.Fatalf("the runner answered op=%s retry=%v ticket=%q, want return_failed retry=true ticket=17",
			got.Op, got.Retry, got.LendID)
	}
	if got.Reason != "the lent memory is still in use" {
		t.Fatalf("the refusal reads %q, want the engine's reason", got.Reason)
	}
	path, auth, body := stub.saw()
	if path != "/vram/return" || auth != "Bearer sk-test" || body != `{"id":"17"}` {
		t.Fatalf("the engine saw %s with %q and %s, want /vram/return with the run's key and the ticket", path, auth, body)
	}
}

// A run whose entry has no lend calls says so instead of pretending, and never
// touches the engine: the broker falls back to the drain ladder.
func TestLendAskOnARunWithoutLendCallsIsRefused(t *testing.T) {
	o := OptionsFor(&config.Engine{Type: "vllm", Name: "maya", URL: "http://127.0.0.1:1"}, Over{})
	got := askOn(t, o, proto.Msg{Op: proto.OpLend, Needs: 4 << 30, Free: 2048})

	if got.Op != proto.OpLendFailed || got.Kind != "Unsupported" {
		t.Fatalf("the runner answered op=%s kind=%q, want lend_failed kind=Unsupported", got.Op, got.Kind)
	}
}
