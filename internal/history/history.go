// Package history keeps the node's recent samples in memory for the
// harbor's graphs: an hour at 5 s by default. Nothing is written to disk
// (by design for now); a restart starts the graphs over.
package history

import (
	"sync"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

type Recorder struct {
	Every time.Duration
	Keep  int

	mu      sync.Mutex
	samples []proto.Sample
}

func (r *Recorder) keep() int {
	if r.Keep <= 0 {
		return 720
	}
	return r.Keep
}

// Add appends one sample, dropping the oldest past Keep.
func (r *Recorder) Add(s proto.Sample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) == r.keep() {
		copy(r.samples, r.samples[1:])
		r.samples = r.samples[:r.keep()-1]
	}
	r.samples = append(r.samples, s)
}

// Get returns a copy of the samples, oldest first.
func (r *Recorder) Get() proto.History {
	r.mu.Lock()
	defer r.mu.Unlock()
	return proto.History{Every: r.Every.Seconds(), Samples: append([]proto.Sample(nil), r.samples...)}
}

// Sample builds one point from a status reading.
func Sample(st proto.Status) proto.Sample {
	s := proto.Sample{T: time.Now(), Waiting: len(st.Waiting)}
	for _, g := range st.GPUs {
		s.GPUs = append(s.GPUs, proto.GPUSample{Index: g.Index, Used: g.Used, Util: g.Util, Temp: g.Temp, Power: g.Power})
	}
	for _, l := range st.Leases {
		if l.Used > 0 {
			if s.Holders == nil {
				s.Holders = map[string]uint64{}
			}
			s.Holders[l.Engine] += l.Used
		}
	}
	for _, q := range st.Squatters {
		s.Squat += q.Used
	}
	if st.Insights != nil {
		s.RPM, s.P95MS = st.Insights.RPM, st.Insights.P95MS
	}
	if st.Host != nil {
		s.CPU, s.RAMUsed = st.Host.CPU, st.Host.RAMUsed
	}
	for _, e := range st.Engines {
		if !e.Up {
			continue // a down engine has nothing resident: its spans close
		}
		if s.Decode == nil {
			s.Decode = map[string]float64{}
		}
		s.Decode[e.Name] = e.DecodeTPS
		s.Models = residentModels(s.Models, e)
		if kv, ok := engineKV(e); ok {
			if s.KV == nil {
				s.KV = map[string]proto.KVStat{}
			}
			s.KV[e.Name] = kv
		}
	}
	return s
}

// engineKV is one engine's KV state at a sample instant: its window budget,
// the windows of the models it reports resident, and the deepest slot fill
// (used over window) of the sessions it reports via /slots. It is absent
// when the engine reports none of the three, so an engine without /slots
// and /props keeps the samples lean.
func engineKV(e proto.Engine) (proto.KVStat, bool) {
	st := proto.KVStat{Window: e.Window}
	for _, md := range e.Models {
		if (md.State != "ready" && md.State != "loaded") || md.Window <= 0 {
			continue
		}
		if st.Models == nil {
			st.Models = map[string]int{}
		}
		st.Models[md.Name] = md.Window
	}
	for _, sl := range e.Slots {
		if sl.Window <= 0 {
			continue
		}
		p := float64(sl.Used) / float64(sl.Window)
		if p < 0 {
			p = 0
		}
		if p > 1 {
			p = 1 // a session deeper than its window clamps to full, not past it
		}
		if p > st.Pressure {
			st.Pressure = p
		}
	}
	return st, st.Window > 0 || st.Pressure > 0 || len(st.Models) > 0
}

// residentModels adds the engine to the sample's model table: models the
// reader says are actually loaded (llama-swap's "ready" upstreams, ollama's
// /api/ps entries) count; anything merely listed does not. The engine is
// present even with no resident model, so a span can close.
func residentModels(m map[string][]string, e proto.Engine) map[string][]string {
	names := []string{}
	for _, md := range e.Models {
		if md.State == "ready" || md.State == "loaded" {
			names = append(names, md.Name)
		}
	}
	if m == nil {
		m = map[string][]string{}
	}
	m[e.Name] = names
	return m
}
