// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package alerts

import (
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
)

type change struct {
	key    Key
	level  Level
	raised bool
	msg    string
	card   string
}

type recorder struct {
	changes []change
}

func (r *recorder) on(key Key, level Level, raised bool, msg, card string) {
	r.changes = append(r.changes, change{key: key, level: level, raised: raised, msg: msg, card: card})
}

func (r *recorder) raised() int {
	n := 0
	for _, c := range r.changes {
		if c.raised {
			n++
		}
	}
	return n
}

func (r *recorder) cleared() int {
	n := 0
	for _, c := range r.changes {
		if !c.raised {
			n++
		}
	}
	return n
}

func levelOf(s *proto.Alerts, k Key) string {
	return s.Levels[string(k)]
}

func one(key Key, v float64) Reading {
	return Reading{V: map[Key]float64{key: v}, Note: map[Key]string{}}
}

// TestHysteresis is the named proof: a value crossing warn fires once;
// flapping around the threshold does not re-fire (every crossing above
// warn resets the clean streak); three consecutive clean polls drop the
// level and re-arm the key, which then fires again on the next crossing.
func TestHysteresis(t *testing.T) {
	rec := &recorder{}
	ev := New(map[Key]proto.Threshold{KeyTemp: {Warn: 10, Crit: 20}}, 3, rec.on)
	// value, level after the poll, raised and cleared counts so far
	cases := []struct {
		v        float64
		level    Level
		raisedN  int
		clearedN int
	}{
		{11, LevelWarn, 1, 0}, // crosses warn: one fire
		{9, LevelWarn, 1, 0},  // flaps below: streak 1
		{11, LevelWarn, 1, 0}, // flaps back: streak reset
		{9, LevelWarn, 1, 0},
		{11, LevelWarn, 1, 0},
		{9, LevelWarn, 1, 0},
		{11, LevelWarn, 1, 0},
		{9, LevelWarn, 1, 0},  // streak 1
		{9, LevelWarn, 1, 0},  // streak 2
		{9, LevelOK, 1, 1},    // streak 3: drops, one clear, re-armed
		{11, LevelWarn, 2, 1}, // crossing again: fires once more
	}
	for i, c := range cases {
		ev.Tick(one(KeyTemp, c.v))
		if lv := lvLevel(levelOf(ev.Snapshot(), KeyTemp)); lv != c.level {
			t.Errorf("poll %d (v=%v): level = %v, want %v", i+1, c.v, lv, c.level)
		}
		if n := rec.raised(); n != c.raisedN {
			t.Errorf("poll %d (v=%v): raised fires = %d, want %d", i+1, c.v, n, c.raisedN)
		}
		if n := rec.cleared(); n != c.clearedN {
			t.Errorf("poll %d (v=%v): cleared fires = %d, want %d", i+1, c.v, n, c.clearedN)
		}
	}
}

func lvLevel(s string) Level {
	switch s {
	case "warn":
		return LevelWarn
	case "crit":
		return LevelCrit
	}
	return LevelOK
}

// TestEscalation: a value that crosses crit while warn is held jumps
// straight to crit; three clean polls drop one level (crit to warn),
// three more drop to ok.
func TestEscalation(t *testing.T) {
	rec := &recorder{}
	ev := New(map[Key]proto.Threshold{KeyTemp: {Warn: 10, Crit: 20}}, 3, rec.on)
	ev.Tick(one(KeyTemp, 15))
	ev.Tick(one(KeyTemp, 21)) // crosses crit: jumps from warn to crit
	for i := 0; i < 3; i++ {
		ev.Tick(one(KeyTemp, 15)) // target warn < crit
	}
	for i := 0; i < 3; i++ {
		ev.Tick(one(KeyTemp, 9)) // target ok < warn
	}
	want := []change{
		{KeyTemp, LevelWarn, true, "temp 15°C (warn at 10°C)", ""},
		{KeyTemp, LevelCrit, true, "temp 21°C (crit at 20°C)", ""},
		{KeyTemp, LevelWarn, false, "temp 15°C (warn at 10°C)", ""},
		{KeyTemp, LevelOK, false, "temp 9°C (warn at 10°C)", ""},
	}
	if len(rec.changes) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(rec.changes), len(want), rec.changes)
	}
	for i, w := range want {
		if rec.changes[i] != w {
			t.Errorf("change %d = %+v, want %+v", i, rec.changes[i], w)
		}
	}
	if lv := levelOf(ev.Snapshot(), KeyTemp); lv != "" {
		t.Errorf("level = %q, want ok (absent)", lv)
	}
}

// TestLowBadKey: cache is bad at or below its thresholds; a low value
// raises, three polls back above warn clear it.
func TestLowBadKey(t *testing.T) {
	rec := &recorder{}
	ev := New(map[Key]proto.Threshold{KeyCache: {Warn: 50}}, 3, rec.on)
	ev.Tick(one(KeyCache, 40)) // 40 <= 50: raises
	if lv := levelOf(ev.Snapshot(), KeyCache); lv != "warn" {
		t.Errorf("level = %q, want warn", lv)
	}
	ev.Tick(one(KeyCache, 55))
	ev.Tick(one(KeyCache, 55))
	ev.Tick(one(KeyCache, 55)) // three clean polls: drops
	if lv := levelOf(ev.Snapshot(), KeyCache); lv != "" {
		t.Errorf("level = %q, want ok (absent)", lv)
	}
	if n := rec.raised(); n != 1 {
		t.Errorf("raised = %d, want 1", n)
	}
	if n := rec.cleared(); n != 1 {
		t.Errorf("cleared = %d, want 1", n)
	}
}

// TestMissingNeverRaises: a key absent from the reading behaves like a
// clean poll; present values raise, absence then clears.
func TestMissingNeverRaises(t *testing.T) {
	rec := &recorder{}
	ev := New(map[Key]proto.Threshold{KeyQueue: {Warn: 2}}, 1, rec.on)
	ev.Tick(Reading{V: map[Key]float64{}})
	ev.Tick(Reading{V: map[Key]float64{}})
	if len(rec.changes) != 0 {
		t.Fatalf("missing values raised: %+v", rec.changes)
	}
	ev.Tick(one(KeyQueue, 5))
	if lv := levelOf(ev.Snapshot(), KeyQueue); lv != "warn" {
		t.Errorf("level = %q, want warn", lv)
	}
	ev.Tick(Reading{V: map[Key]float64{}}) // absent = one clean poll (ClearPolls 1)
	if lv := levelOf(ev.Snapshot(), KeyQueue); lv != "" {
		t.Errorf("level = %q, want ok", lv)
	}
}

// TestBuiltInsAlwaysOn: with no operator table the built-in danger
// keys are still active; a probe failure lands straight on crit on the
// first poll, and the message is the probe error itself.
func TestBuiltInsAlwaysOn(t *testing.T) {
	rec := &recorder{}
	ev := New(FromConfig(config.AlertsConfig{}), 3, rec.on)
	r := Reading{V: map[Key]float64{KeyProbe: 1}, Note: map[Key]string{KeyProbe: "nvidia-smi: no devices found"}}
	ev.Tick(r)
	snap := ev.Snapshot()
	if snap.Levels["probe"] != "crit" {
		t.Errorf("probe level = %q, want crit", snap.Levels["probe"])
	}
	if _, ok := snap.Table["probe"]; !ok {
		t.Error("table lacks the built-in probe key")
	}
	if _, ok := snap.Table["stuck"]; !ok {
		t.Error("table lacks the built-in stuck key")
	}
	if len(rec.changes) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(rec.changes), rec.changes)
	}
	c := rec.changes[0]
	if c.key != KeyProbe || c.level != LevelCrit || !c.raised || c.msg != "nvidia-smi: no devices found" {
		t.Errorf("change = %+v", c)
	}
}

// TestFromStatus pins the exact reading rules: the scheduled GPU's
// values, power absent when the limit is unreported, cache only while
// there was traffic, the deepest slot fill as kv, and the notes.
func TestFromStatus(t *testing.T) {
	total := uint64(1000)
	st := proto.Status{
		GPUs: []proto.GPU{
			{Index: 0, Name: "other", Total: 500, Used: 100, Temp: -1},
			{Index: 1, Name: "card", Scheduled: true, Total: total, Used: total * 9 / 10, Temp: 85, Power: 0, PowerLimit: 0},
		},
		Waiting:  []proto.Lease{{Engine: "a"}, {Engine: "b"}},
		Leases:   []proto.Lease{{Engine: "stuck-one", State: "stuck"}},
		ProbeErr: "nvidia-smi: timeout",
		Insights: &proto.Insights{
			Requests5m:  3,
			PrefillMS:   1234,
			TPOTMS:      55,
			P95MS:       900,
			CachePct:    42,
			ReingestPct: 58,
		},
		Engines: []proto.Engine{
			{Name: "swap", Slots: []proto.Slot{{ID: "s1", Window: 10, Used: 9}}},
			{Name: "quiet"}, // no slots: contributes nothing to kv
		},
	}
	r := FromStatus(st)
	want := map[Key]float64{
		KeyTemp: 85, KeyVRAM: 0.9, KeyQueue: 2, KeyProbe: 1, KeyStuck: 1,
		KeyCache: 42, KeyReingest: 58, KeyTTFT: 1234, KeyTPOT: 55, KeyP95: 900,
		KeyKV: 0.9,
	}
	for k, w := range want {
		if got := r.V[k]; got != w {
			t.Errorf("V[%q] = %v, want %v", k, got, w)
		}
	}
	if _, ok := r.V[KeyPower]; ok {
		t.Error("power present with an unreported limit")
	}
	if r.Note[KeyProbe] != "nvidia-smi: timeout" {
		t.Errorf("Note[probe] = %q", r.Note[KeyProbe])
	}
	if r.Note[KeyStuck] != "stuck-one" {
		t.Errorf("Note[stuck] = %q", r.Note[KeyStuck])
	}
}

// TestFromStatusIdle: with no GPUs and an idle engine (no request
// traffic) the reading carries the always-present keys, cache stays
// absent (so a quiet night never alarms), and the other insight keys
// carry 0, which cannot cross a high-bad threshold.
func TestFromStatusIdle(t *testing.T) {
	st := proto.Status{
		GPUs:    []proto.GPU{},
		Waiting: []proto.Lease{},
		Leases:  []proto.Lease{},
		Insights: &proto.Insights{
			Requests5m: 0,
			CachePct:   0,
		},
	}
	r := FromStatus(st)
	for _, k := range []Key{KeyQueue, KeyProbe, KeyStuck} {
		if _, ok := r.V[k]; !ok {
			t.Errorf("%q not present", k)
		}
	}
	for _, k := range []Key{KeyCache, KeyVRAM, KeyTemp, KeyPower, KeyKV} {
		if _, ok := r.V[k]; ok {
			t.Errorf("%q present on an empty status", k)
		}
	}
	for _, k := range []Key{KeyTTFT, KeyTPOT, KeyP95, KeyReingest} {
		if v, ok := r.V[k]; !ok || v != 0 {
			t.Errorf("%q = %v (present=%v), want 0", k, v, ok)
		}
	}
	if r.V[KeyProbe] != 0 || r.V[KeyStuck] != 0 || r.V[KeyQueue] != 0 {
		t.Errorf("V = %v", r.V)
	}
	// No insights at all: none of the insight keys are present.
	r2 := FromStatus(proto.Status{})
	for _, k := range []Key{KeyTTFT, KeyTPOT, KeyP95, KeyCache, KeyReingest} {
		if _, ok := r2.V[k]; ok {
			t.Errorf("%q present without insights", k)
		}
	}
}

// With several cards on the node, each level reads the worst card, and the
// note names it, so a hot gpu1 alerts while gpu0 is cool.
func TestFromStatusWorstCard(t *testing.T) {
	st := proto.Status{GPUs: []proto.GPU{
		{Index: 0, Card: "gpu0", Scheduled: true, Total: 1000, Used: 500, Temp: 60, Power: 100, PowerLimit: 400},
		{Index: 1, Card: "gpu1", Scheduled: true, Total: 1000, Used: 950, Temp: 88, Power: 300, PowerLimit: 400},
	}}
	r := FromStatus(st)
	if r.V[KeyTemp] != 88 || r.V[KeyVRAM] != 0.95 || r.V[KeyPower] != 0.75 {
		t.Fatalf("V = %v", r.V)
	}
	for _, k := range []Key{KeyTemp, KeyVRAM, KeyPower} {
		if r.Card[k] != "gpu1" {
			t.Errorf("Card[%q] = %q, want gpu1", k, r.Card[k])
		}
		if r.Note[k] != "gpu1" {
			t.Errorf("Note[%q] = %q, want gpu1", k, r.Note[k])
		}
	}
	if msg := msgFor(KeyTemp, proto.Threshold{Warn: 85, Crit: 95}, LevelWarn, r); !strings.HasSuffix(msg, " on gpu1") {
		t.Errorf("message = %q, want it to name gpu1", msg)
	}
	if r := FromStatus(proto.Status{GPUs: st.GPUs[1:]}); r.Note[KeyTemp] != "" {
		t.Errorf("one card: Note[temp] = %q, want none, so one-card messages stay as they were", r.Note[KeyTemp])
	} else if r.Card[KeyTemp] != "gpu1" {
		t.Errorf("one card: Card[temp] = %q, want gpu1", r.Card[KeyTemp])
	}
}
