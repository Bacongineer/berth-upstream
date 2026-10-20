// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/fsx"
	"github.com/T-Crypt/berth/internal/procs"
)

// engineRec is one engine the gateway started as its own process: the run
// process's PID and start time, and the lease it holds. The record
// outlives the gateway, so the next one can tell a live engine from a
// stale note and adopt the live one instead of starting it again.
type engineRec struct {
	Engine  string    `json:"engine"`
	PID     int       `json:"pid"`
	Start   uint64    `json:"pid_start"`
	Lease   string    `json:"lease,omitempty"`
	Started time.Time `json:"started"`
}

// adopt re-marks engines a previous gateway started and that still run:
// the record's process is the same one (PID plus start time) and the
// engine answers. The next request is proxied, not started again.
// Records whose process left are stale and dropped. Windows adopts
// nothing: its process start times are not readable.
func (g *Gateway) adopt() {
	recs := g.readRecords()
	if len(recs) == 0 {
		return
	}
	var live []engineRec
	for _, rec := range recs {
		start := procs.System{}.StartTime(rec.PID)
		if rec.Start == 0 || start != rec.Start {
			g.log().Info("gateway drops a stale engine record", "engine", rec.Engine, "pid", rec.PID)
			continue
		}
		live = append(live, rec)
		e := g.engineByName(rec.Engine)
		if e == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		up := g.probe(ctx, e)
		cancel()
		if !up {
			continue // still loading or wedged; the next start reads it again
		}
		s := &started{exited: make(chan struct{}), stderr: &lockedBuf{}, output: &tailBuf{}, answered: true}
		g.mu.Lock()
		if g.running == nil {
			g.running = map[string]*started{}
		}
		g.running[rec.Engine] = s
		g.mu.Unlock()
		go g.watchAdopted(rec, s)
		g.log().Info("gateway adopted a running engine", "engine", rec.Engine, "pid", rec.PID, "lease", rec.Lease)
	}
	g.keepRecords(live)
}

// watchAdopted drops an adopted engine when its run process leaves: the
// lease went with it, and the engine may be started again.
func (g *Gateway) watchAdopted(rec engineRec, s *started) {
	for {
		time.Sleep(2 * time.Second)
		start := procs.System{}.StartTime(rec.PID)
		if start == rec.Start {
			continue
		}
		g.mu.Lock()
		if cur := g.running[rec.Engine]; cur == s {
			delete(g.running, rec.Engine)
		}
		g.mu.Unlock()
		g.dropRecord(rec.Engine)
		close(s.exited)
		return
	}
}

func (g *Gateway) engineByName(name string) *config.Engine {
	for i := range g.Engines {
		if g.Engines[i].Name == name {
			return &g.Engines[i]
		}
	}
	return nil
}

// putRecord notes a started run; setLease completes it when the run is
// granted its berth; drop ends it. Each rewrite leaves the file with the
// engines a coming gateway may adopt.
func (g *Gateway) putRecord(rec engineRec) {
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	if g.records == nil {
		g.records = map[string]engineRec{}
	}
	g.records[rec.Engine] = rec
	g.saveRecords()
}

func (g *Gateway) setLease(name, lease string) {
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	rec, ok := g.records[name]
	if !ok || rec.Lease == lease {
		return
	}
	rec.Lease = lease
	g.records[name] = rec
	g.saveRecords()
}

func (g *Gateway) dropRecord(name string) {
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	if _, ok := g.records[name]; !ok {
		return
	}
	delete(g.records, name)
	g.saveRecords()
}

func (g *Gateway) keepRecords(recs []engineRec) {
	g.stateMu.Lock()
	defer g.stateMu.Unlock()
	g.records = map[string]engineRec{}
	for _, rec := range recs {
		g.records[rec.Engine] = rec
	}
	g.saveRecords()
}

// saveRecords writes the file atomically. The state mutex is held.
func (g *Gateway) saveRecords() {
	if g.StateFile == "" {
		return
	}
	names := make([]string, 0, len(g.records))
	for n := range g.records {
		names = append(names, n)
	}
	sort.Strings(names)
	recs := make([]engineRec, 0, len(names))
	for _, n := range names {
		recs = append(recs, g.records[n])
	}
	b, err := json.Marshal(recs)
	if err != nil {
		return
	}
	if err := fsx.WriteFile(g.StateFile, b, 0o600); err != nil {
		g.log().Warn("gateway could not write its engine records", "path", g.StateFile, "err", err)
	}
}

func (g *Gateway) readRecords() []engineRec {
	if g.StateFile == "" {
		return nil
	}
	b, err := os.ReadFile(g.StateFile)
	if err != nil {
		return nil
	}
	var recs []engineRec
	if json.Unmarshal(b, &recs) != nil {
		return nil
	}
	return recs
}
