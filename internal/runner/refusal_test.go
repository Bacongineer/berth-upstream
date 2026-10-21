// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

func TestRefusedBoundaryRearmsRun(t *testing.T) {
	var idle atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if idle.Load() {
			fmt.Fprint(w, `[{"is_processing":false}]`)
		} else {
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	sock := filepath.Join(t.TempDir(), "broker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var logs syncBuf
	exited := make(chan int, 1)
	go func() {
		exited <- Run(Options{Socket: sock, Engine: "fixture", DrainSlots: srv.URL, FailClosed: true, Grace: 20 * time.Millisecond, Retry: time.Millisecond, Stderr: &logs, Argv: []string{"sleep", "30"}})
	}()
	raw, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := raw.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	conn := proto.NewConn(raw)
	if _, err := conn.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpGranted, Lease: "fixture-1"}); err != nil {
		t.Fatal(err)
	}
	attached, err := conn.Recv()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if attached.PID > 0 {
			_ = syscall.Kill(attached.PID, syscall.SIGKILL)
		}
	})
	if err := conn.Send(proto.Msg{Op: proto.OpDrain, Reason: "first"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "boundary refusal", func() bool { return strings.Contains(logs.String(), "drain refused") })
	select {
	case code := <-exited:
		t.Fatalf("refusal stopped the child: %d", code)
	default:
	}
	idle.Store(true)
	if err := conn.Send(proto.Msg{Op: proto.OpDrain, Reason: "second"}); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 143 {
			t.Fatalf("exit = %d; %s", code, logs.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("refused drain did not rearm: %s", logs.String())
	}
}
