// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server_test

// What a run sends over the socket is what the lease owns: a Compose
// engine's whole project arrives as a set of container IDs.

import (
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/server"
)

const (
	ocrID     = "5f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f9b5f6c4d3e2a1b0f9e8d7c6b5a"
	sidecarID = "3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d"
)

func TestAttachSendsTheWholeContainerSet(t *testing.T) {
	card := &parkCard{total: 24 << 30}
	sock, b := parkDaemon(t, card)

	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpAcquire, Engine: "paddleocr", Needs: 8 << 30}); err != nil {
		t.Fatal(err)
	}
	if g, err := conn.Recv(); err != nil || g.Op != proto.OpGranted {
		t.Fatalf("acquire = %v %v", g, err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpAttach, PID: 101, Containers: []string{ocrID, sidecarID}}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, "the project on the lease", func() bool {
		ls := b.Status().Leases
		return len(ls) == 1 && strings.Join(ls[0].Containers, ",") == ocrID+","+sidecarID
	})
}

// A berth run started before an upgrade attaches with the single
// "container" field; the new daemon still puts that container on the lease.
func TestAnOlderWrapperStillNamesItsContainer(t *testing.T) {
	card := &parkCard{total: 24 << 30}
	sock, b := parkDaemon(t, card)

	conn, raw, err := server.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := conn.Send(proto.Msg{Op: proto.OpAcquire, Engine: "tagger", Needs: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	if g, err := conn.Recv(); err != nil || g.Op != proto.OpGranted {
		t.Fatalf("acquire = %v %v", g, err)
	}
	if err := conn.Send(proto.Msg{Op: proto.OpAttach, PID: 101, Container: ocrID}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the old wrapper's container on the lease", func() bool {
		ls := b.Status().Leases
		return len(ls) == 1 && strings.Join(ls[0].Containers, ",") == ocrID
	})
}
