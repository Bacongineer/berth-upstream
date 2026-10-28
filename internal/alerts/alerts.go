// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package alerts is the node's threshold table and its sticky level
// state. The daemon evaluates the table on its sample cycle; the levels
// it holds are what the harbor paints and what the operator is
// notified about, so the node and the UI cannot disagree. probe and
// stuck are built-in danger keys, always on; the KPI keys are enabled
// by the operator's [alerts] table.
package alerts

import (
	"fmt"
	"sort"
	"strings"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
)

// Key is one alert key: a KPI the operator can threshold, or a built-in
// danger state.
type Key string

const (
	KeyQueue    Key = "queue"
	KeyTTFT     Key = "ttft"
	KeyTPOT     Key = "tpot"
	KeyP95      Key = "p95"
	KeyCache    Key = "cache"
	KeyReingest Key = "reingest"
	KeyVRAM     Key = "vram"
	KeyTemp     Key = "temp"
	KeyPower    Key = "power"
	KeyKV       Key = "kv"
	KeyProbe    Key = "probe"
	KeyStuck    Key = "stuck"
)

// dir is a key's bad direction, a property of the key fixed in code:
// high keys are bad at or above their thresholds, cache is bad at or
// below them.
type dir int

const (
	dirHigh dir = iota
	dirLow
)

var directions = map[Key]dir{
	KeyQueue: dirHigh, KeyTTFT: dirHigh, KeyTPOT: dirHigh, KeyP95: dirHigh,
	KeyCache:    dirLow,
	KeyReingest: dirHigh, KeyVRAM: dirHigh, KeyTemp: dirHigh, KeyPower: dirHigh,
	KeyKV: dirHigh, KeyProbe: dirHigh, KeyStuck: dirHigh,
}

// builtIn are the danger keys always in the node's table. Their warn
// and crit are both 1: any non-zero value crosses both, so a crossing
// lands straight on crit.
var builtIn = map[Key]proto.Threshold{
	KeyProbe: {Warn: 1, Crit: 1},
	KeyStuck: {Warn: 1, Crit: 1},
}

// FromConfig is the node's threshold table: the built-in danger keys
// plus every KPI key the operator enabled (a key is enabled when warn
// or crit is non-zero).
func FromConfig(c config.AlertsConfig) map[Key]proto.Threshold {
	table := map[Key]proto.Threshold{
		KeyProbe: builtIn[KeyProbe],
		KeyStuck: builtIn[KeyStuck],
	}
	keys := []struct {
		key Key
		thr proto.Threshold
	}{
		{KeyQueue, c.Queue},
		{KeyTTFT, c.TTFT},
		{KeyTPOT, c.TPOT},
		{KeyP95, c.P95},
		{KeyCache, c.Cache},
		{KeyReingest, c.Reingest},
		{KeyVRAM, c.VRAM},
		{KeyTemp, c.Temp},
		{KeyPower, c.Power},
		{KeyKV, c.KV},
	}
	for _, k := range keys {
		if k.thr.Warn != 0 || k.thr.Crit != 0 {
			table[k.key] = k.thr
		}
	}
	return table
}

// Reading is one poll of the values the table can evaluate. V holds the
// keys actually reported; absence never raises. Note carries human
// context (the probe error, the stuck lease engines) for the messages.
// Card names the card a card-level key was read from, even with one card.
type Reading struct {
	V    map[Key]float64
	Note map[Key]string
	Card map[Key]string
}

// FromStatus reads one status into a Reading: the exact values the node
// would evaluate and notify on. The card levels read the worst scheduled
// GPU, else the first GPU; power is present only when the
// driver reports both watts and a limit; cache is present only while
// there was request traffic, so an idle engine never alarms.
func FromStatus(st proto.Status) Reading {
	r := Reading{V: map[Key]float64{}, Note: map[Key]string{}, Card: map[Key]string{}}
	var cards []proto.GPU
	for _, g := range st.GPUs {
		if g.Scheduled {
			cards = append(cards, g)
		}
	}
	if len(cards) == 0 && len(st.GPUs) > 0 {
		cards = st.GPUs[:1]
	}
	// Each card level reads the worst card; with several, the note names it.
	worst := func(k Key, v float64, g proto.GPU) {
		if old, ok := r.V[k]; ok && old >= v {
			return
		}
		r.V[k] = v
		r.Card[k] = g.Card
		if len(cards) > 1 {
			r.Note[k] = g.Card
		}
	}
	for _, g := range cards {
		if g.Temp >= 0 {
			worst(KeyTemp, g.Temp, g)
		}
		if g.Power > 0 && g.PowerLimit > 0 {
			worst(KeyPower, g.Power/g.PowerLimit, g)
		}
		if g.Total > 0 {
			worst(KeyVRAM, float64(g.Used)/float64(g.Total), g)
		}
	}
	r.V[KeyQueue] = float64(len(st.Waiting))
	if st.ProbeErr != "" {
		r.V[KeyProbe] = 1
		r.Note[KeyProbe] = st.ProbeErr
	} else {
		r.V[KeyProbe] = 0
	}
	var stuck []string
	for _, l := range st.Leases {
		if l.State == "stuck" {
			stuck = append(stuck, l.Engine)
		}
	}
	for _, l := range st.Waiting {
		if l.State == "stuck" {
			stuck = append(stuck, l.Engine)
		}
	}
	sort.Strings(stuck)
	r.V[KeyStuck] = float64(len(stuck))
	if len(stuck) > 0 {
		r.Note[KeyStuck] = strings.Join(stuck, ", ")
	}
	if in := st.Insights; in != nil {
		if in.Requests5m > 0 {
			r.V[KeyCache] = in.CachePct
		}
		r.V[KeyTTFT] = in.PrefillMS
		r.V[KeyTPOT] = in.TPOTMS
		r.V[KeyP95] = in.P95MS
		r.V[KeyReingest] = in.ReingestPct
	}
	best := 0.0
	have := false
	for _, e := range st.Engines {
		for _, s := range e.Slots {
			if s.Window > 0 {
				have = true
				if p := float64(s.Used) / float64(s.Window); p > best {
					best = p
				}
			}
		}
	}
	if have {
		r.V[KeyKV] = best
	}
	return r
}

// Level is one key's sticky alert level: ok < warn < crit.
type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelCrit
)

// Name is the level's wire name.
func (l Level) Name() string {
	return [...]string{"ok", "warn", "crit"}[l]
}

// Evaluator is the sticky level state over the table: a key raises the
// first poll it crosses a level, and drops a level only after
// ClearPolls consecutive clean polls. One goroutine owns it (the
// daemon's sampler), so it needs no lock.
type Evaluator struct {
	Table      map[Key]proto.Threshold
	ClearPolls int
	OnChange   func(key Key, level Level, raised bool, msg, card string)

	level map[Key]Level
	clean map[Key]int
}

// New builds an Evaluator over table. A nil on is a no-op;
// clearPolls <= 0 becomes 3.
func New(table map[Key]proto.Threshold, clearPolls int, on func(key Key, level Level, raised bool, msg, card string)) *Evaluator {
	if clearPolls <= 0 {
		clearPolls = 3
	}
	return &Evaluator{Table: table, ClearPolls: clearPolls, OnChange: on, level: map[Key]Level{}, clean: map[Key]int{}}
}

func crosses(d dir, v, thr float64) bool {
	if d == dirLow {
		return v <= thr
	}
	return v >= thr
}

// Tick advances the evaluator one poll over r and returns the keys
// currently at a non-ok level. A key missing from r.V behaves like a
// clean poll; absence never raises.
func (e *Evaluator) Tick(r Reading) map[Key]Level {
	out := map[Key]Level{}
	for key, thr := range e.Table {
		target := LevelOK
		if v, ok := r.V[key]; ok {
			if thr.Crit != 0 && crosses(directions[key], v, thr.Crit) {
				target = LevelCrit
			} else if thr.Warn != 0 && crosses(directions[key], v, thr.Warn) {
				target = LevelWarn
			}
		}
		lv := e.level[key]
		switch {
		case target > lv:
			e.level[key] = target
			e.clean[key] = 0
			e.fire(key, target, true, msgFor(key, thr, target, r), r.Card[key])
		case target == lv:
			e.clean[key] = 0
		default: // target < lv: one more clean poll toward a drop
			e.clean[key]++
			if e.clean[key] >= e.ClearPolls {
				e.level[key] = target
				e.clean[key] = 0
				e.fire(key, target, false, msgFor(key, thr, target, r), r.Card[key])
			}
		}
		if l := e.level[key]; l != LevelOK {
			out[key] = l
		}
	}
	return out
}

func (e *Evaluator) fire(key Key, level Level, raised bool, msg, card string) {
	if e.OnChange == nil {
		return
	}
	e.OnChange(key, level, raised, msg, card)
}

// Snapshot is the wire form: the table and the keys held at a non-ok
// level, key -> level name.
func (e *Evaluator) Snapshot() *proto.Alerts {
	a := &proto.Alerts{Table: make(map[string]proto.Threshold, len(e.Table)), Levels: map[string]string{}}
	for k, t := range e.Table {
		a.Table[string(k)] = t
	}
	for k, l := range e.level {
		if l != LevelOK {
			a.Levels[string(k)] = l.Name()
		}
	}
	return a
}

// msgFor composes the message a level change for key is reported with:
// the value the node evaluated and the boundary involved. For a drop
// to ok the named boundary is the warn level (or crit when warn is not
// set), the first one the value would have to cross back through.
func msgFor(key Key, thr proto.Threshold, level Level, r Reading) string {
	switch key {
	case KeyProbe:
		if level == LevelOK {
			return "gpu probe recovered"
		}
		if n := r.Note[KeyProbe]; n != "" {
			return n
		}
		return "gpu probe failing"
	case KeyStuck:
		if level == LevelOK {
			return "all leases free"
		}
		if n := r.Note[KeyStuck]; n != "" {
			return n
		}
		return "leases aground"
	}
	named := level
	if named == LevelOK {
		if thr.Warn != 0 {
			named = LevelWarn
		} else {
			named = LevelCrit
		}
	}
	boundary := thr.Warn
	if named == LevelCrit {
		boundary = thr.Crit
	}
	msg := fmt.Sprintf("%s %s (%s at %s)", key, valueText(key, r.V[key]), named.Name(), thrText(key, boundary))
	if card := r.Note[key]; card != "" {
		msg += " on " + card
	}
	return msg
}

func valueText(key Key, v float64) string {
	switch key {
	case KeyQueue:
		return fmt.Sprintf("%.0f waiting", v)
	case KeyTTFT, KeyTPOT, KeyP95:
		return fmt.Sprintf("%g ms", v)
	case KeyCache, KeyReingest:
		return fmt.Sprintf("%g%%", v)
	case KeyVRAM, KeyPower, KeyKV:
		return fmt.Sprintf("%g of the limit", v)
	case KeyTemp:
		return fmt.Sprintf("%g°C", v)
	}
	return fmt.Sprintf("%g", v)
}

func thrText(key Key, v float64) string {
	switch key {
	case KeyQueue:
		return fmt.Sprintf("%.0f", v)
	case KeyTTFT, KeyTPOT, KeyP95:
		return fmt.Sprintf("%g ms", v)
	case KeyCache, KeyReingest:
		return fmt.Sprintf("%g%%", v)
	case KeyVRAM, KeyPower, KeyKV:
		return fmt.Sprintf("%g of the limit", v)
	case KeyTemp:
		return fmt.Sprintf("%g°C", v)
	}
	return fmt.Sprintf("%g", v)
}
