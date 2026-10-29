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
		`berth_gpu_memory_total_bytes{gpu="0",card="gpu0",name="Fake",scheduled="true"} 25769803776`,
		`berth_leases{card="gpu0",state="active"} 0`,
		`berth_waiting{card="gpu0"} 0`,
		`berth_build_info{version="`,
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

func TestWritePromPerCard(t *testing.T) {
	st := proto.Status{
		Version: "v9.9.9",
		GPUs: []proto.GPU{
			{Card: "gpu0", Index: 0, Name: `Card "A"`, Scheduled: true, Total: 100, Used: 40, Free: 60, Baseline: 5,
				Util: 50, Temp: 61, Power: 120.5, PowerLimit: 300, Fan: -1},
			{Card: "gpu1", Index: 1, Name: "Card B", Scheduled: true, Total: 200, Used: 80, Free: 120, Baseline: 7,
				Util: 25, Temp: 55, Power: 90, PowerLimit: 250, Fan: 40},
		},
		Leases: []proto.Lease{
			{Card: "gpu1", ID: "a", Engine: "llama-swap", Priority: "normal", State: "active", Used: 30},
			{Card: "gpu0", ID: "b", Engine: "comfy", Priority: "low", State: "parked", Used: 10},
		},
		Waiting:   []proto.Lease{{Card: "gpu1", ID: "w", Engine: "strata"}},
		Squatters: []proto.Squatter{{Card: "gpu0", PID: 42, Name: "game", Used: 7}},
	}
	var buf strings.Builder
	WriteProm(&buf, st)
	out := buf.String()
	for _, want := range []string{
		`berth_gpu_memory_total_bytes{gpu="1",card="gpu1",name="Card B",scheduled="true"} 200`,
		`berth_gpu_memory_used_bytes{gpu="0",card="gpu0",name="Card \"A\"",scheduled="true"} 40`,
		`berth_gpu_baseline_bytes{gpu="0",card="gpu0"} 5`,
		`berth_gpu_baseline_bytes{gpu="1",card="gpu1"} 7`,
		`berth_gpu_utilization_ratio{gpu="0",card="gpu0"} 0.5`,
		`berth_gpu_temperature_celsius{gpu="1",card="gpu1"} 55`,
		`berth_gpu_power_watts{gpu="0",card="gpu0"} 120.5`,
		`berth_gpu_power_limit_watts{gpu="1",card="gpu1"} 250`,
		`berth_gpu_fan_ratio{gpu="1",card="gpu1"} 0.4`,
		`berth_leases{card="gpu0",state="parked"} 1`,
		`berth_leases{card="gpu0",state="active"} 0`,
		`berth_leases{card="gpu1",state="active"} 1`,
		`berth_leases{card="gpu1",state="stuck"} 0`,
		`berth_lease_used_bytes{card="gpu1",engine="llama-swap",priority="normal",state="active"} 30`,
		`berth_lease_used_bytes{card="gpu0",engine="comfy",priority="low",state="parked"} 10`,
		`berth_waiting{card="gpu0"} 0`,
		`berth_waiting{card="gpu1"} 1`,
		`berth_squatters{card="gpu0"} 1`,
		`berth_squatters{card="gpu1"} 0`,
		`berth_squatter_bytes{card="gpu0"} 7`,
		`berth_squatter_bytes{card="gpu1"} 0`,
		`berth_build_info{version="v9.9.9"} 1`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("metrics lack %s\n%s", want, out)
		}
	}
	if strings.Contains(out, `berth_gpu_fan_ratio{gpu="0"`) {
		t.Errorf("a fan the driver did not report got a series\n%s", out)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if seen[line] {
			t.Errorf("line appears twice: %s", line)
		}
		seen[line] = true
	}
}

func TestWritePromSumsLeasesAndSkipsEmptyFamilies(t *testing.T) {
	st := proto.Status{
		GPUs: []proto.GPU{{Card: "gpu0", Scheduled: true, Util: -1, Temp: -1, Power: -1, PowerLimit: -1, Fan: -1}},
		Leases: []proto.Lease{
			{Card: "gpu0", ID: "a", Engine: "e", Priority: "normal", State: "active", Used: 3},
			{Card: "gpu0", ID: "b", Engine: "e", Priority: "normal", State: "active", Used: 4},
		},
	}
	var buf strings.Builder
	WriteProm(&buf, st)
	out := buf.String()
	if !strings.Contains(out, `berth_lease_used_bytes{card="gpu0",engine="e",priority="normal",state="active"} 7`+"\n") {
		t.Errorf("same-label leases were not summed\n%s", out)
	}
	for _, name := range []string{"utilization_ratio", "temperature_celsius", "power_watts", "power_limit_watts", "fan_ratio"} {
		if strings.Contains(out, "berth_gpu_"+name) {
			t.Errorf("family %s printed with no series\n%s", name, out)
		}
	}
}
