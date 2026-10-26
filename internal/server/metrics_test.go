// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/node"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

type oneCard struct{}

func (oneCard) Read(context.Context) (gpu.Snapshot, error) {
	return gpu.Snapshot{Total: 24 << 30, Free: 24 << 30, At: time.Now(), Devices: []gpu.Device{{Total: 24 << 30, Free: 24 << 30, Name: "Fake"}}}, nil
}

func TestMetricsLoopbackOnly(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	b := broker.New(broker.Config{Log: log}, oneCard{}, procs.Map{})
	b.Tick(context.Background())
	if err := ServeMetrics(context.Background(), "0.0.0.0:0", node.One(b), nil, log); err == nil {
		t.Fatal("a non-loopback listen address was accepted")
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engines := func() []proto.Engine {
		return []proto.Engine{{Name: "llama-swap", Type: "llama-swap", Up: true, DecodeTPS: 125}}
	}
	go ServeMetrics(ctx, addr, node.One(b), engines, log)
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{
		`berth_gpu_memory_total_bytes{gpu="0",name="Fake",scheduled="true"} 25769803776`,
		`berth_collisions_avoided_total 0`,
		`berth_engine_decode_tokens_per_second{engine="llama-swap",type="llama-swap",source="engine"} 125`,
		`berth_probe_up 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("metrics lack %s\n%s", want, body)
		}
	}

	// DNS rebinding: a page on evil.example resolving to 127.0.0.1 sends its own Host.
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/metrics", nil)
	req.Host = "evil.example:80"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host got %s", resp.Status)
	}
}
