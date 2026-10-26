// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package server_test

import (
	"testing"

	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

// A request names its card on the socket. A card the node does not have is
// refused with the node's cards listed; the first card answers by name as it
// does with no card named.
func TestAcquireNamesItsCard(t *testing.T) {
	sock := start(t)
	ask := func(card string) proto.Msg {
		t.Helper()
		conn, raw, err := server.Dial(sock)
		if err != nil {
			t.Fatal(err)
		}
		defer raw.Close()
		if err := conn.Send(proto.Msg{Op: proto.OpAcquire, Engine: "vllm", Priority: "batch", Card: card}); err != nil {
			t.Fatal(err)
		}
		m, err := conn.Recv()
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := ask("gpu7"); m.Op != proto.OpRefused || m.Reason != "no card gpu7 on this node (cards: gpu0)" {
		t.Fatalf("acquire on gpu7 = %s %q, want refused naming the node's cards", m.Op, m.Reason)
	}
	if m := ask("gpu0"); m.Op != proto.OpGranted {
		t.Fatalf("acquire on gpu0 = %s %q, want granted", m.Op, m.Reason)
	}
}
