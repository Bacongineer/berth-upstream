package history

import (
	"testing"

	"github.com/T-Crypt/berth/internal/proto"
)

func TestRingAndSample(t *testing.T) {
	r := &Recorder{Keep: 3}
	st := proto.Status{
		GPUs:      []proto.GPU{{Index: 0, Used: 10, Util: 90}},
		Leases:    []proto.Lease{{Engine: "ninfer", Used: 8}, {Engine: "ninfer", Used: 1}, {Engine: "idle"}},
		Squatters: []proto.Squatter{{Used: 2}},
		Engines:   []proto.Engine{{Name: "llama-swap", Up: true, DecodeTPS: 120}, {Name: "strata"}},
		Host:      &proto.Host{CPU: 12},
	}
	for i := 0; i < 5; i++ {
		r.Add(Sample(st))
	}
	h := r.Get()
	if len(h.Samples) != 3 {
		t.Fatalf("kept %d", len(h.Samples))
	}
	s := h.Samples[2]
	if s.Holders["ninfer"] != 9 || len(s.Holders) != 1 || s.Squat != 2 || s.Decode["llama-swap"] != 120 || len(s.Decode) != 1 || s.CPU != 12 || s.GPUs[0].Util != 90 {
		t.Fatalf("sample = %+v", s)
	}
}

// TestSampleModels: only resident models count -- llama-swap's "ready"
// upstreams and ollama's loaded entries; an up engine with nothing loaded is
// still present (empty list), and a down engine has no model data at all.
func TestSampleModels(t *testing.T) {
	st := proto.Status{
		Engines: []proto.Engine{
			{Name: "llama-swap", Type: "llama-swap", Up: true, Models: []proto.Model{
				{Name: "A-27B", State: "ready"},
				{Name: "B-8B", State: "loading"},
			}},
			{Name: "ollama", Type: "ollama", Up: true, Models: []proto.Model{{Name: "qwen3:8b", State: "loaded"}}},
			{Name: "strata", Type: "llamacpp", Up: true, Models: []proto.Model{}},
			{Name: "ninfer", Type: "ninfer", Up: false, Models: []proto.Model{{Name: "C", State: "ready"}}},
		},
	}
	s := Sample(st)
	if len(s.Models["llama-swap"]) != 1 || s.Models["llama-swap"][0] != "A-27B" {
		t.Fatalf("llama-swap = %v, want only the ready model", s.Models["llama-swap"])
	}
	if len(s.Models["ollama"]) != 1 || s.Models["ollama"][0] != "qwen3:8b" {
		t.Fatalf("ollama = %v", s.Models["ollama"])
	}
	ms, ok := s.Models["strata"]
	if !ok || len(ms) != 0 {
		t.Fatalf("strata = %v, want present and empty", s.Models["strata"])
	}
	if _, ok := s.Models["ninfer"]; ok {
		t.Fatalf("down engine %v must not carry model data", s.Models["ninfer"])
	}
}

// TestSampleKV: an up engine folds into the sample's KV table - its window,
// its resident models' windows, and the deepest slot fill; a down engine
// stays out of the table entirely.
func TestSampleKV(t *testing.T) {
	st := proto.Status{
		Engines: []proto.Engine{
			{
				Name: "ninfer", Type: "ninfer", Up: true, Window: 8192,
				Models: []proto.Model{{Name: "A-27B", State: "ready", Window: 4096}, {Name: "B-8B", State: "loading", Window: 8192}},
				Slots:  []proto.Slot{{ID: "s0", Window: 8192, Used: 4096}, {ID: "s1", Window: 8000, Used: 7600}},
			},
			{Name: "strata", Type: "strata", Up: false},
		},
	}
	s := Sample(st)
	kv, ok := s.KV["ninfer"]
	if !ok {
		t.Fatalf("up engine missing from KV table: %+v", s.KV)
	}
	if kv.Window != 8192 {
		t.Fatalf("window = %d, want 8192", kv.Window)
	}
	// 7600/8000 = 0.95 beats 4096/8192 = 0.5
	if kv.Pressure < 0.95-1e-9 || kv.Pressure > 0.95+1e-9 {
		t.Fatalf("pressure = %v, want 0.95", kv.Pressure)
	}
	if len(kv.Models) != 1 || kv.Models["A-27B"] != 4096 {
		t.Fatalf("models = %+v, want only the ready model's window", kv.Models)
	}
	if _, ok := s.KV["strata"]; ok {
		t.Fatalf("down engine %v must not carry KV data", s.KV["strata"])
	}
}

// TestSampleKVClampsToFull: a session the engine reports deeper than its
// window clamps to 1, not past it.
func TestSampleKVClampsToFull(t *testing.T) {
	st := proto.Status{Engines: []proto.Engine{{
		Name: "kobold", Type: "llamacpp", Up: true,
		Slots: []proto.Slot{{ID: "s0", Window: 1000, Used: 2000}},
	}}}
	s := Sample(st)
	kv, ok := s.KV["kobold"]
	if !ok {
		t.Fatalf("engine missing from KV table: %+v", s.KV)
	}
	if kv.Pressure != 1 {
		t.Fatalf("pressure = %v, want the clamp at 1", kv.Pressure)
	}
}
