// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package proto_test

import (
	"bytes"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

// OpStop and its reply share the wire: the engine name goes out on the ask,
// the leases asked come back on the reply.
func TestStopRoundTripsOverTheWire(t *testing.T) {
	out := &bytes.Buffer{}
	if err := proto.NewConn(out).Send(proto.Msg{Op: proto.OpStop, Engine: "ninfer"}); err != nil {
		t.Fatal(err)
	}
	js := out.String()
	for _, want := range []string{`"op":"stop"`, `"engine":"ninfer"`} {
		if !strings.Contains(js, want) {
			t.Errorf("ask %q lacks %q", js, want)
		}
	}

	reply := &bytes.Buffer{}
	leases := []string{"ninfer-1", "ninfer-2"}
	if err := proto.NewConn(reply).Send(proto.Msg{Op: proto.OpStop, Engine: "ninfer", Leases: leases}); err != nil {
		t.Fatal(err)
	}
	got, err := proto.NewConn(bytes.NewBufferString(reply.String())).Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.Op != proto.OpStop || got.Engine != "ninfer" || len(got.Leases) != len(leases) ||
		got.Leases[0] != "ninfer-1" || got.Leases[1] != "ninfer-2" {
		t.Fatalf("reply = %+v", got)
	}
}

// A writer without deadline support sends exactly like Send.
func TestSendWithDeadlinePlainWriterMatchesSend(t *testing.T) {
	var a, b bytes.Buffer
	m := proto.Msg{Op: proto.OpStatus}
	if err := proto.NewConn(rw(&a)).SendWithDeadline(m, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := proto.NewConn(rw(&b)).Send(m); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() || a.Len() == 0 {
		t.Fatalf("deadline send differs from Send: %q vs %q", a.String(), b.String())
	}
}

// A reader that never reads trips the deadline instead of wedging the
// writer: the error is a timeout, and it arrives near the deadline.
func TestSendWithDeadlineTimesOutOnSlowReader(t *testing.T) {
	r, w := net.Pipe()
	defer r.Close()
	defer w.Close()
	c := proto.NewConn(w)
	start := time.Now()
	err := c.SendWithDeadline(proto.Msg{Op: proto.OpEvent}, 200*time.Millisecond)
	el := time.Since(start)
	if err == nil {
		t.Fatal("slow reader was never dropped: SendWithDeadline succeeded")
	}
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if el > 5*time.Second {
		t.Fatalf("slow reader wedged the writer for %s", el)
	}
}

type rwBuf struct{ *bytes.Buffer }

func rw(b *bytes.Buffer) *rwBuf { return &rwBuf{b} }

func (r *rwBuf) Read(p []byte) (int, error) { return 0, os.ErrClosed }
