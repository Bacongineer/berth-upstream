package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/engines"
	"github.com/T-Crypt/berth/internal/insights"
	"github.com/T-Crypt/berth/internal/proto"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "harbor.db"), 720*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// sampleAt is one 5 s reading: used climbs with i, everything else is flat,
// so a minute's fold has exact expected values. Its KV state alternates
// with i (0.5, 0.9, 0.5, ...) on one engine, so two adjacent minutes fold
// to different pressures.
func sampleAt(t time.Time, i int) proto.Sample {
	return proto.Sample{
		T:       t,
		GPUs:    []proto.GPUSample{{Index: 0, Used: uint64(1000 * (i + 1)), Util: 50, Temp: 60, Power: 200}},
		Holders: map[string]uint64{"agent": uint64(100 * (i + 1))},
		Squat:   uint64(10 * (i + 1)),
		Waiting: i % 2,
		CPU:     30,
		RAMUsed: uint64(1000 * (i + 1)),
		Decode:  map[string]float64{"agent": 10 * float64(i+1)},
		KV: map[string]proto.KVStat{"agent": {
			Window:   4096,
			Pressure: 0.5 + 0.4*float64(i%2),
			Models:   map[string]int{"m1": 4096},
		}},
		RPM:   2,
		P95MS: float64(100 * (i + 1)),
	}
}

// minuteRow is one samples_1m row straight out of the database.
type minuteRow struct {
	gpus, holders, decode, kv string
	sq, cpu, ram, rpm, p95    float64
	wm, n                     int
}

func (s *Store) row(t *testing.T, minute int64) (minuteRow, bool) {
	t.Helper()
	var r minuteRow
	err := s.db.QueryRow(`SELECT gpus, holders, squat_avg, waiting_max, cpu_avg, ram_avg, decode, rpm_avg, p95_max, kv, n
		FROM samples_1m WHERE minute = ?`, minute).
		Scan(&r.gpus, &r.holders, &r.sq, &r.wm, &r.cpu, &r.ram, &r.decode, &r.rpm, &r.p95, &r.kv, &r.n)
	if err != nil {
		return minuteRow{}, false
	}
	return r, true
}

func dump1m(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT minute, gpus, holders, squat_avg, waiting_max, cpu_avg, ram_avg, decode, rpm_avg, p95_max, kv, n FROM samples_1m ORDER BY minute`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m minuteRow
		var min int64
		if err := rows.Scan(&min, &m.gpus, &m.holders, &m.sq, &m.wm, &m.cpu, &m.ram, &m.decode, &m.rpm, &m.p95, &m.kv, &m.n); err != nil {
			t.Fatal(err)
		}
		out = append(out, strconv.FormatInt(min, 10)+" "+m.gpus+" "+m.holders+" "+m.decode+" "+m.kv)
	}
	return join(out)
}

// kvOf decodes a row's kv column; the agent's stat is what sampleAt folds.
func kvAgent(t *testing.T, rowStr string) proto.KVStat {
	t.Helper()
	var m map[string]proto.KVStat
	if err := json.Unmarshal([]byte(rowStr), &m); err != nil {
		t.Fatalf("kv %q: %v", rowStr, err)
	}
	return m["agent"]
}

func join(xs []string) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += "|"
		}
		s += x
	}
	return s
}

func splitDump(d string) []string {
	var out []string
	cur := ""
	for _, c := range d {
		if c == '|' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}

func req(id string, at time.Time, out float64) proto.Request {
	return proto.Request{ID: id, Engine: "swap", Model: "NInfer", T: at, Status: 200,
		Prompt: 100, Cache: 50, Output: out, DecodeTPS: 50, DurationMS: 1000, PrefillTPS: 500, PrefillMS: 50}
}

// TestMinuteAggregation checks the fold math and that the newest sample's
// minute is never stored (it is still open).
func TestMinuteAggregation(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	m0 := now.Truncate(time.Minute).Add(-2 * time.Minute)
	samples := []proto.Sample{
		sampleAt(m0.Add(5*time.Second), 0),
		sampleAt(m0.Add(40*time.Second), 1),
		sampleAt(m0.Add(60*time.Second+10*time.Second), 2), // opens minute 1, so minute 0 closes
	}
	if err := s.IngestSamples(samples); err != nil {
		t.Fatal(err)
	}
	r, ok := s.row(t, m0.Unix())
	if !ok {
		t.Fatal("minute 0 not stored")
	}
	// sample i=0: used 1000, holder 100, squat 10, waiting 0, ram 1000, decode 10, p95 100
	// sample i=1: used 2000, holder 200, squat 20, waiting 1, ram 2000, decode 20, p95 200
	if r.n != 2 || r.sq != 15 || r.wm != 1 || r.cpu != 30 || r.ram != 1500 || r.rpm != 2 || r.p95 != 200 {
		t.Fatalf("folded %+#v", r)
	}
	var gs []gpuStat
	if err := json.Unmarshal([]byte(r.gpus), &gs); err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].Index != 0 || gs[0].Used != 1500 || gs[0].UsedMax != 2000 ||
		gs[0].Util != 50 || gs[0].Temp != 60 || gs[0].Power != 200 {
		t.Fatalf("gpus %s", r.gpus)
	}
	if got := map[string]float64{}; json.Unmarshal([]byte(r.holders), &got) != nil || len(got) != 1 || got["agent"] != 150 {
		t.Fatalf("holders %s", r.holders)
	}
	if got := map[string]float64{}; json.Unmarshal([]byte(r.decode), &got) != nil || got["agent"] != 15 {
		t.Fatalf("decode %s", r.decode)
	}
	if _, ok := s.row(t, m0.Add(time.Minute).Unix()); ok {
		t.Fatal("the newest sample's minute must stay open")
	}
}

// TestIdempotentReIngest: overlapping windows may be re-ingested any number
// of times; stored minutes keep their first, complete values.
func TestIdempotentReIngest(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	m0 := now.Truncate(time.Minute).Add(-3 * time.Minute)
	batch := []proto.Sample{sampleAt(m0.Add(5*time.Second), 0), sampleAt(m0.Add(50*time.Second), 1),
		sampleAt(m0.Add(65*time.Second), 2), sampleAt(m0.Add(130*time.Second), 3),
		sampleAt(m0.Add(190*time.Second), 4)}
	for i := 0; i < 3; i++ {
		if err := s.IngestSamples(batch); err != nil {
			t.Fatal(err)
		}
	}
	// the batch's newest sample sits in minute 2, so only minutes 0 and 1
	// closed; triple-ingesting must have left exactly those two rows
	first := dump1m(t, s)
	r0, ok0 := s.row(t, m0.Unix())
	if !ok0 || r0.n != 2 || r0.p95 != 200 {
		t.Fatalf("minutes after triple ingest:\n%s", first)
	}
	if err := s.IngestSamples(append(append([]proto.Sample(nil), batch...), sampleAt(m0.Add(4*time.Minute+10*time.Second), 5))); err != nil {
		t.Fatal(err)
	}
	// the new sample closes minutes 2 and 3; their rows fold only the
	// samples those minutes got (i=3, i=4), not the whole re-sent batch
	r2, ok2 := s.row(t, m0.Add(2*time.Minute).Unix())
	r3, ok3 := s.row(t, m0.Add(3*time.Minute).Unix())
	if !ok2 || !ok3 {
		t.Fatalf("expected minutes 2 and 3 stored:\n%s", dump1m(t, s))
	}
	if r2.n != 1 || r2.p95 != 400 || r3.n != 1 || r3.p95 != 500 {
		t.Fatalf("r2 %+#v r3 %+#v", r2, r3)
	}
	before, after := splitDump(first), splitDump(dump1m(t, s))
	if len(after) != 4 || after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("stored minutes drifted after re-ingest:\nbefore %s\nafter  %s", first, after)
	}
}

// TestRequestDedupe: the engine's id is the table's key, so replays insert
// nothing new and IngestRequests counts only the rows it really wrote.
func TestRequestDedupe(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	reqs := []proto.Request{req("swap/1", now.Add(-3*time.Hour), 10), req("swap/2", now.Add(-2*time.Hour), 20), req("swap/3", now.Add(-time.Hour), 30)}
	if n, err := s.IngestRequests(reqs); err != nil || n != 3 {
		t.Fatalf("first ingest: n=%d err=%v", n, err)
	}
	if n, _ := s.IngestRequests(reqs); n != 0 {
		t.Fatalf("replay inserted %d rows", n)
	}
	mixed := append(append([]proto.Request{}, reqs...), req("swap/4", now.Add(-30*time.Minute), 40))
	if n, _ := s.IngestRequests(mixed); n != 1 {
		t.Fatalf("mixed batch inserted %d rows, want 1", n)
	}
	var c int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&c); err != nil || c != 4 {
		t.Fatalf("table has %d rows, want 4", c)
	}
}

// TestHourlyRollup: one hour of minute rows folds into one 1 h row; the
// hour in progress never folds, and a repeat pass is a no-op.
func TestHourlyRollup(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	base := now.Truncate(time.Hour).Add(-time.Hour) // the fully closed hour before this one
	var samples []proto.Sample
	for i := 0; i < 5; i++ {
		samples = append(samples, sampleAt(base.Add(time.Duration(i)*time.Minute+10*time.Second), i))
	}
	samples = append(samples, sampleAt(base.Add(5*time.Minute+40*time.Second), 5)) // opens the hour's next minute
	if err := s.IngestSamples(samples); err != nil {
		t.Fatal(err)
	}
	if err := s.RollupHours(now); err != nil {
		t.Fatal(err)
	}
	var used, usedMax, n int64
	var gpus string
	err := s.db.QueryRow(`SELECT gpus, n FROM samples_1h WHERE hour = ?`, base.Unix()).Scan(&gpus, &n)
	if err != nil {
		t.Fatalf("no 1 h row for %s: %v", base, err)
	}
	// samples i=0..4: used 1000..5000 -> mean 3000, peak 5000, n 5
	var gs []gpuStat
	if json.Unmarshal([]byte(gpus), &gs) != nil || len(gs) != 1 {
		t.Fatalf("gpus %s", gpus)
	}
	used = int64(gs[0].Used)
	usedMax = int64(gs[0].UsedMax)
	if used != 3000 || usedMax != 5000 || n != 5 {
		t.Fatalf("hour row used=%d max=%d n=%d", used, usedMax, n)
	}
	if err := s.RollupHours(now); err != nil { // a repeat pass must not move it
		t.Fatal(err)
	}
	n2 := 0
	s.db.QueryRow(`SELECT n FROM samples_1h WHERE hour = ?`, base.Unix()).Scan(&n2)
	if n2 != 5 {
		t.Fatalf("repeat pass changed the row: n=%d", n2)
	}
	h, err := s.History(7*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if h.Every != 3600 {
		t.Fatalf("7 d range must read the 1 h table, every=%v", h.Every)
	}
	found := false
	for _, sp := range h.Samples {
		if sp.T.Equal(base) && len(sp.GPUs) == 1 && sp.GPUs[0].Used == 3000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("7 d history missing the hour row: %+v", h.Samples)
	}
}

// TestPrune drops what is past the retention, keeping the hour rollups out
// to the fixed 400 d mark.
func TestPrune(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	oldM := now.Add(-31 * 24 * time.Hour).Truncate(time.Minute) // past the 30 d default used here
	rM := now.Add(-2 * time.Minute).Truncate(time.Minute)       // a closed recent minute
	if err := s.IngestSamples([]proto.Sample{
		sampleAt(oldM.Add(5*time.Second), 0),
		sampleAt(oldM.Add(50*time.Second), 1),
		sampleAt(rM.Add(5*time.Second), 2),
		sampleAt(rM.Add(50*time.Second), 3),
		sampleAt(now, 4), // newest minute stays open, closing the two above
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestRequests([]proto.Request{req("swap/o", oldM, 1), req("swap/r", rM, 2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(now, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.row(t, oldM.Unix()); ok {
		t.Fatal("old minute row survived the prune")
	}
	if _, ok := s.row(t, rM.Unix()); !ok {
		t.Fatal("recent minute row was pruned")
	}
	var c int
	s.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE id = 'swap/o'`).Scan(&c)
	if c != 0 {
		t.Fatal("old request survived the prune")
	}
	s.db.QueryRow(`SELECT COUNT(*) FROM requests WHERE id = 'swap/r'`).Scan(&c)
	if c != 1 {
		t.Fatal("recent request was pruned")
	}
}

// TestRangeSelection: 24 h and shorter read the 1 m table, longer the 1 h
// table, and the range starts at its first bucket boundary.
func TestRangeSelection(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	m0 := now.Truncate(time.Minute).Add(-5 * time.Minute)
	samples := make([]proto.Sample, 0, 10)
	for i := 0; i < 6; i++ {
		samples = append(samples, sampleAt(m0.Add(time.Duration(i)*time.Minute+10*time.Second), i))
	}
	samples = append(samples, sampleAt(m0.Add(6*time.Minute+10*time.Second), 6))
	if err := s.IngestSamples(samples); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if h.Every != 60 {
		t.Fatalf("24 h and under must read the 1 m table, every=%v", h.Every)
	}
	// all six closed minutes fit inside one hour; the range keeps the ones
	// inside the last hour, oldest first
	if len(h.Samples) != 6 || !h.Samples[0].T.Before(h.Samples[1].T) {
		t.Fatalf("got %d samples, ordering %v", len(h.Samples), h.Samples)
	}
}

// spanSample is a sample carrying only the model table: which engine reports
// which models resident at at. An engine present with an empty slice is up
// with nothing loaded; an absent engine has no model data.
func spanSample(at time.Time, models map[string][]string) proto.Sample {
	return proto.Sample{T: at, Models: models}
}

// spanDump is model_spans straight out of the database, one line per row.
func (s *Store) spanDump(t *testing.T) string {
	t.Helper()
	rows, err := s.db.Query(`SELECT engine, model, start, end, last_seen FROM model_spans ORDER BY engine, model, start`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var engine, model string
		var st, ls int64
		var en sql.NullInt64
		if err := rows.Scan(&engine, &model, &st, &en, &ls); err != nil {
			t.Fatal(err)
		}
		e := "open"
		if en.Valid {
			e = strconv.FormatInt(en.Int64, 10)
		}
		out = append(out, engine+"/"+model+" ["+strconv.FormatInt(st, 10)+" .. "+e+"] seen "+strconv.FormatInt(ls, 10))
	}
	return join(out)
}

// spanOf finds one span in the API result by engine and model; it must
// exist exactly once in the window given.
func spanOf(t *testing.T, sp []Span, engine, model string) Span {
	t.Helper()
	var hit []Span
	for _, x := range sp {
		if x.Engine == engine && x.Model == model {
			hit = append(hit, x)
		}
	}
	if len(hit) != 1 {
		t.Fatalf("want exactly one %s/%s span, got %d", engine, model, len(hit))
	}
	return hit[0]
}

// TestSpanLifecycle: a model seen opens a span, sightings bump last_seen,
// and a sample that reports the engine without the model closes it at its
// last_seen. An empty per-engine slice closes just like a missing model.
func TestSpanLifecycle(t *testing.T) {
	s := openStore(t)
	now := time.Now().Truncate(time.Second)
	t0 := now.Add(-10 * time.Minute)
	steps := []proto.Sample{
		spanSample(t0, map[string][]string{"llama-swap": {"A"}, "ollama": {}}),
		spanSample(t0.Add(30*time.Second), map[string][]string{"llama-swap": {"A"}, "ollama": {}}),
		spanSample(t0.Add(time.Minute), map[string][]string{"llama-swap": {"A"}, "ollama": {"qwen3:8b"}}),
		spanSample(t0.Add(2*time.Minute), map[string][]string{"llama-swap": {}, "ollama": {"qwen3:8b"}}),
		spanSample(t0.Add(5*time.Minute), map[string][]string{"llama-swap": {"B"}, "ollama": {}}),
	}
	if err := s.IngestSpans(steps); err != nil {
		t.Fatal(err)
	}
	sp, err := s.Spans(time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	a := spanOf(t, sp, "llama-swap", "A")
	if !a.Start.Equal(t0) || a.End == nil || !a.End.Equal(t0.Add(time.Minute)) {
		t.Fatalf("A = %+v, want [%s .. %s] (closed at its last_seen)", a, t0, t0.Add(time.Minute))
	}
	q := spanOf(t, sp, "ollama", "qwen3:8b")
	if !q.Start.Equal(t0.Add(time.Minute)) || q.End == nil || !q.End.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("qwen3 = %+v", q)
	}
	b := spanOf(t, sp, "llama-swap", "B")
	if !b.Start.Equal(t0.Add(5*time.Minute)) || b.End != nil {
		t.Fatalf("B = %+v, want open from %s", b, t0.Add(5*time.Minute))
	}
	// a model never seen on an engine that is only down has no span
	if _, err := s.Spans(time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if n := spanCount(sp, "strata", "x"); n != 0 {
		t.Fatalf("strata span exists: %s", s.spanDump(t))
	}
}

func spanCount(sp []Span, engine, model string) int {
	var n int
	for _, x := range sp {
		if x.Engine == engine && x.Model == model {
			n++
		}
	}
	return n
}

// TestSpanIdempotentReIngest: the collector re-reads the node's whole last
// hour every minute, so overlapping windows may arrive any number of times;
// the watermark keeps every replay a no-op.
func TestSpanIdempotentReIngest(t *testing.T) {
	s := openStore(t)
	now := time.Now().Truncate(time.Second)
	t0 := now.Add(-8 * time.Minute)
	win1 := []proto.Sample{
		spanSample(t0, map[string][]string{"llama-swap": {"A"}}),
		spanSample(t0.Add(40*time.Second), map[string][]string{"llama-swap": {"A"}}),
	}
	win2 := append(append([]proto.Sample{}, win1...),
		spanSample(t0.Add(time.Minute+20*time.Second), map[string][]string{"llama-swap": {"A"}}),
		spanSample(t0.Add(2*time.Minute), map[string][]string{"llama-swap": {"B"}}),
	)
	for _, w := range [][]proto.Sample{win1, win2, win1, win2} {
		if err := s.IngestSpans(w); err != nil {
			t.Fatal(err)
		}
	}
	sp, err := s.Spans(time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	// A was seen through t0+1m20s and then absent: one closed span, no
	// duplicates from the re-reads
	a := spanOf(t, sp, "llama-swap", "A")
	if !a.Start.Equal(t0) || a.End == nil || !a.End.Equal(t0.Add(time.Minute+20*time.Second)) {
		t.Fatalf("A = %+v\n%s", a, s.spanDump(t))
	}
	if n := spanCount(sp, "llama-swap", "A"); n != 1 {
		t.Fatalf("A spans = %d: %s", n, s.spanDump(t))
	}
	b := spanOf(t, sp, "llama-swap", "B")
	if b.End != nil || !b.Start.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("B = %+v", b)
	}
}

// TestSpanGapSplit: a hole between sightings wider than the gap threshold
// means the harbor or the node was down; the old span ends at its last
// sighting and a new one starts, so the down time is not drawn as loaded.
func TestSpanGapSplit(t *testing.T) {
	s := openStore(t)
	now := time.Now().Truncate(time.Second)
	t0 := now.Add(-15 * time.Minute)
	if err := s.IngestSpans([]proto.Sample{
		spanSample(t0, map[string][]string{"llama-swap": {"A"}}),
		// ten minutes with no samples at all: the harbor was down
		spanSample(t0.Add(10*time.Minute), map[string][]string{"llama-swap": {"A"}}),
	}); err != nil {
		t.Fatal(err)
	}
	sp, err := s.Spans(time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	var a []Span
	for _, x := range sp {
		if x.Engine == "llama-swap" && x.Model == "A" {
			a = append(a, x)
		}
	}
	if len(a) != 2 {
		t.Fatalf("want the gap to split A into two spans, got %d:\n%s", len(a), s.spanDump(t))
	}
	if !a[0].Start.Equal(t0) || a[0].End == nil || !a[0].End.Equal(t0) {
		t.Fatalf("first span = %+v, want [%s .. %s]", a[0], t0, t0)
	}
	if !a[1].Start.Equal(t0.Add(10*time.Minute)) || a[1].End != nil {
		t.Fatalf("second span = %+v, want open from %s", a[1], t0.Add(10*time.Minute))
	}
}

// TestSpanPruneAndRange: spans that ended past the retention drop, an open
// span survives the prune, and the range query returns exactly the spans
// overlapping the window, open spans among them.
func TestSpanPruneAndRange(t *testing.T) {
	s := openStore(t)
	now := time.Now().Truncate(time.Second)
	old := now.Add(-31 * 24 * time.Hour) // past the 30 d retention used here
	if err := s.IngestSpans([]proto.Sample{
		spanSample(old, map[string][]string{"ollama": {"old"}}),
		spanSample(old.Add(time.Minute), map[string][]string{"ollama": {}}), // closes "old" long ago
		spanSample(now.Add(-4*time.Hour), map[string][]string{"ollama": {"mid"}}),
		spanSample(now.Add(-3*time.Hour), map[string][]string{"ollama": {}}),              // closes "mid" three hours ago
		spanSample(now.Add(-30*time.Minute), map[string][]string{"llama-swap": {"live"}}), // still open
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(now, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	sp, err := s.Spans(6*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if n := spanCount(sp, "ollama", "old"); n != 0 {
		t.Fatalf("pruned span came back: %s", s.spanDump(t))
	}
	if n := spanCount(sp, "ollama", "mid"); n != 1 {
		t.Fatalf("recent closed span: %d\n%s", n, s.spanDump(t))
	}
	live := spanOf(t, sp, "llama-swap", "live")
	if live.End != nil {
		t.Fatalf("open span must survive the prune: %+v", live)
	}
	// a 2 h window excludes the span that ended three hours before its edge
	sp6, err := s.Spans(2*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if n := spanCount(sp6, "ollama", "mid"); n != 0 {
		t.Fatalf("mid must sit outside a 2 h range: %s", s.spanDump(t))
	}
	if n := spanCount(sp6, "llama-swap", "live"); n != 1 {
		t.Fatalf("the open span overlaps every range: %s", s.spanDump(t))
	}
}

// TestInsightsMatchTheNode: over the same requests, the store's computation
// is the node's. The node side is a real Watcher polling a fake engine that
// replays exactly these requests, so both sides run their own path: the
// store over its rows, the node over its in-memory log.
func TestInsightsMatchTheNode(t *testing.T) {
	s := openStore(t)
	now := time.Now().Truncate(time.Second) // the store keeps whole seconds, so must the fixtures
	tr := []proto.Request{
		{ID: "swap/1", Engine: "swap", Model: "A", T: now.Add(-3 * time.Hour), Status: 200, Prompt: 1000, Cache: 900, Output: 200, Draft: 100, DraftAcc: 80, PrefillTPS: 1000, DecodeTPS: 100, DurationMS: 3000, PrefillMS: 100},
		{ID: "swap/2", Engine: "swap", Model: "A", T: now.Add(-2 * time.Hour), Status: 200, Prompt: 1000, Cache: 0, Output: 100, PrefillTPS: 2000, DecodeTPS: 120, DurationMS: 1000, PrefillMS: 500},
		{ID: "swap/3", Engine: "swap", Model: "B", T: now.Add(-3 * time.Minute), Status: 200, Prompt: 10, Cache: 0, Output: 50, PrefillTPS: 500, DecodeTPS: 300, DurationMS: 500, PrefillMS: 20},
	}
	if _, err := s.IngestRequests(tr); err != nil {
		t.Fatal(err)
	}
	got, err := s.Insights(24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	// The shared math over the very same slice is the store's spec.
	want := insights.Compute(tr, nil, []insights.Col{{Label: "1h", Window: time.Hour}, {Label: "24h", Window: 24 * time.Hour}}, tr[0].T, now)
	gotLedger, wantLedger := got.Ledger, want.Ledger
	gotSince, wantSince := got.Since, want.Since
	got.Ledger, got.Since = nil, time.Time{} // the ledger's range label differs by design; it is checked below
	want.Ledger, want.Since = nil, time.Time{}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("insights differ:\ngot  %s\nwant %s", gb, wb)
	}
	if !gotSince.Equal(wantSince) {
		t.Fatalf("since: got %s want %s", gotSince, wantSince)
	}
	if len(gotLedger) != 2 || len(wantLedger) != 2 {
		t.Fatalf("ledger cols: got %d want %d", len(gotLedger), len(wantLedger))
	}
	for i := range gotLedger {
		if gotLedger[i].Label != wantLedger[i].Label || gotLedger[i].Requests != wantLedger[i].Requests ||
			gotLedger[i].Generated != wantLedger[i].Generated || gotLedger[i].Cached != wantLedger[i].Cached ||
			gotLedger[i].Fresh != wantLedger[i].Fresh {
			t.Fatalf("ledger %d: got %+v want %+v", i, gotLedger[i], wantLedger[i])
		}
	}
	// ...and the node's own in-memory computation: a Watcher that reads the
	// same requests back from a fake llama-swap activity feed.
	var feed []string
	for i, q := range tr {
		feed = append(feed, fmt.Sprintf(
			`{"id":%d,"timestamp":%q,"model":%q,"resp_status_code":%d,"tokens":{"cache_tokens":%v,"draft_tokens":%v,"draft_acc_tokens":%v,"input_tokens":%v,"output_tokens":%v,"prompt_per_second":%v,"tokens_per_second":%v},"duration_ms":%v}`,
			i+1, q.T.Format(time.RFC3339), q.Model, q.Status, q.Cache, q.Draft, q.DraftAcc, q.Prompt-q.Cache, q.Output, q.PrefillTPS, q.DecodeTPS, q.DurationMS))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/running":
			fmt.Fprint(w, `{"running":[]}`)
		case "/api/metrics/activity":
			fmt.Fprint(w, `{"data":[`+strings.Join(feed, ",")+`]}`)
		}
	}))
	defer srv.Close()
	w := &engines.Watcher{Engines: []config.Engine{{Name: "swap", Type: "llama-swap", URL: srv.URL}}}
	w.Poll(context.Background())
	node := w.Insights(now)
	if node == nil {
		t.Fatal("node insights nil")
	}
	if got.Requests5m != node.Requests5m || got.RPM != node.RPM || got.P50MS != node.P50MS ||
		got.P95MS != node.P95MS || got.PrefillMS != node.PrefillMS || got.TPOTMS != node.TPOTMS ||
		got.DecodeTPS != node.DecodeTPS || got.PrefillTPS != node.PrefillTPS || got.CachePct != node.CachePct ||
		got.ReingestPct != node.ReingestPct || got.MTPPct != node.MTPPct {
		t.Fatalf("KPIs differ:\nnode  %+v\nstore %+v", node, got)
	}
	if len(gotLedger) < 2 || len(node.Ledger) < 2 {
		t.Fatalf("ledger too short: node %+v store %+v", node.Ledger, gotLedger)
	}
	for i := 0; i < 2; i++ {
		if gotLedger[i].Requests != node.Ledger[i].Requests || gotLedger[i].Generated != node.Ledger[i].Generated ||
			gotLedger[i].Cached != node.Ledger[i].Cached || gotLedger[i].Fresh != node.Ledger[i].Fresh {
			t.Fatalf("ledger %d: node %+v store %+v", i, node.Ledger[i], gotLedger[i])
		}
	}
	if len(got.Leaderboard) != len(node.Leaderboard) {
		t.Fatalf("leaderboard rows: node %d store %d", len(node.Leaderboard), len(got.Leaderboard))
	}
	for i := range got.Leaderboard {
		if got.Leaderboard[i] != node.Leaderboard[i] {
			t.Fatalf("leaderboard %d: node %+v store %+v", i, node.Leaderboard[i], got.Leaderboard[i])
		}
	}
}

// TestKVFoldAndRollup: the per-minute KV fold takes the peak slot pressure
// of the samples and the last sample's window and models; the hour rollup
// folds the same columns out of the minute rows; both tables read back with
// the fold intact.
func TestKVFoldAndRollup(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	base := now.Truncate(time.Hour).Add(-time.Hour) // the fully closed hour before this one
	samples := []proto.Sample{
		sampleAt(base.Add(5*time.Second), 0),               // minute 0: two samples at 0.5
		sampleAt(base.Add(55*time.Second), 4),              // minute 0: still 0.5 (even i)
		sampleAt(base.Add(time.Minute+5*time.Second), 1),   // minute 1: 0.9
		sampleAt(base.Add(2*time.Minute+5*time.Second), 2), // minute 2 stays open, closing the other two
	}
	if err := s.IngestSamples(samples); err != nil {
		t.Fatal(err)
	}
	r0, ok0 := s.row(t, base.Unix())
	if !ok0 {
		t.Fatalf("minute 0 not stored:\n%s", dump1m(t, s))
	}
	if kv := kvAgent(t, r0.kv); kv.Window != 4096 || kv.Pressure < 0.5-1e-9 || kv.Pressure > 0.5+1e-9 ||
		len(kv.Models) != 1 || kv.Models["m1"] != 4096 {
		t.Fatalf("minute 0 kv = %+v, want window 4096, pressure 0.5, models m1", kv)
	}
	r1, ok1 := s.row(t, base.Add(time.Minute).Unix())
	if !ok1 {
		t.Fatalf("minute 1 not stored:\n%s", dump1m(t, s))
	}
	if kv := kvAgent(t, r1.kv); kv.Pressure < 0.9-1e-9 || kv.Pressure > 0.9+1e-9 {
		t.Fatalf("minute 1 kv = %+v, want pressure 0.9", kv)
	}
	if err := s.RollupHours(now); err != nil {
		t.Fatal(err)
	}
	var gpus, kvH string
	if err := s.db.QueryRow(`SELECT gpus, kv FROM samples_1h WHERE hour = ?`, base.Unix()).Scan(&gpus, &kvH); err != nil {
		t.Fatalf("no 1 h row: %v", err)
	}
	// the hour takes the minutes' peak pressure and the last minute's window
	if kv := kvAgent(t, kvH); kv.Pressure < 0.9-1e-9 || kv.Pressure > 0.9+1e-9 || kv.Window != 4096 || kv.Models["m1"] != 4096 {
		t.Fatalf("hour kv = %+v, want pressure 0.9 window 4096 models m1", kv)
	}
	h, err := s.History(2*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if h.Every != 60 {
		t.Fatalf("2 h range must read the 1 m table, every=%v", h.Every)
	}
	var p0, p1 float64
	for _, sp := range h.Samples {
		if sp.T.Equal(base) {
			p0 = sp.KV["agent"].Pressure
		}
		if sp.T.Equal(base.Add(time.Minute)) {
			p1 = sp.KV["agent"].Pressure
		}
	}
	if p0 < 0.5-1e-9 || p0 > 0.5+1e-9 || p1 < 0.9-1e-9 || p1 > 0.9+1e-9 {
		t.Fatalf("2 h history kv = %v / %v, want 0.5 / 0.9", p0, p1)
	}
	h7, err := s.History(7*24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sp := range h7.Samples {
		if sp.T.Equal(base) {
			found = sp.KV["agent"].Pressure > 0.9-1e-9
		}
	}
	if !found {
		t.Fatalf("7 d history hour row lost its kv fold: %+v", h7.Samples)
	}
}

// TestEmptyKVFoldStoresEmptyJSON: a minute in which no engine reports KV
// folds to the column value '{}' - the same one migrated pre-kv rows carry -
// never 'null'.
func TestEmptyKVFoldStoresEmptyJSON(t *testing.T) {
	s := openStore(t)
	m0 := time.Now().Truncate(time.Minute).Add(-2 * time.Minute)
	empty0 := sampleAt(m0.Add(5*time.Second), 0)
	empty0.KV = nil
	empty1 := sampleAt(m0.Add(40*time.Second), 1)
	empty1.KV = nil
	empty2 := sampleAt(m0.Add(time.Minute+5*time.Second), 2) // closes minute m0
	empty2.KV = nil
	if err := s.IngestSamples([]proto.Sample{empty0, empty1, empty2}); err != nil {
		t.Fatal(err)
	}
	r, ok := s.row(t, m0.Unix())
	if !ok {
		t.Fatalf("minute not stored:\n%s", dump1m(t, s))
	}
	if r.kv != "{}" {
		t.Fatalf("empty fold kv = %q, want '{}'", r.kv)
	}
}

// preKVSchema is what a pre-KV harbor wrote: the schema const minus the kv
// columns. A database created with it must open under the new code, its old
// rows must read back with kv = '{}', and the new rows must carry the fold.
const preKVSchema = `
CREATE TABLE IF NOT EXISTS samples_1m (
	minute INTEGER PRIMARY KEY,
	gpus TEXT NOT NULL,
	holders TEXT NOT NULL,
	squat_avg REAL NOT NULL,
	waiting_max INTEGER NOT NULL,
	cpu_avg REAL NOT NULL,
	ram_avg REAL NOT NULL,
	decode TEXT NOT NULL,
	rpm_avg REAL NOT NULL,
	p95_max REAL NOT NULL,
	n INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS samples_1h (
	hour INTEGER PRIMARY KEY,
	gpus TEXT NOT NULL,
	holders TEXT NOT NULL,
	squat_avg REAL NOT NULL,
	waiting_max INTEGER NOT NULL,
	cpu_avg REAL NOT NULL,
	ram_avg REAL NOT NULL,
	decode TEXT NOT NULL,
	rpm_avg REAL NOT NULL,
	p95_max REAL NOT NULL,
	n INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS requests (
	id TEXT PRIMARY KEY,
	t INTEGER NOT NULL,
	engine TEXT NOT NULL,
	model TEXT,
	status INTEGER,
	prompt REAL,
	cache REAL,
	output REAL,
	draft REAL,
	draft_acc REAL,
	prefill_tps REAL,
	decode_tps REAL,
	duration_ms REAL,
	prefill_ms REAL
);
CREATE INDEX IF NOT EXISTS ix_requests_t ON requests (t);
CREATE TABLE IF NOT EXISTS model_spans (
	id INTEGER PRIMARY KEY,
	engine TEXT NOT NULL,
	model TEXT NOT NULL,
	start INTEGER NOT NULL,
	end INTEGER,
	last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_model_spans_end ON model_spans (end);
CREATE INDEX IF NOT EXISTS ix_model_spans_start ON model_spans (start);
CREATE TABLE IF NOT EXISTS meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

// TestPreKVDatabaseMigrates: a harbor.db from before the KV column is exactly this
// shape - written before the kv column existed. The new binary must open it
// in place, read the old rows back with empty kv, and store new data beside
// them without rewriting anything.
func TestPreKVDatabaseMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbor.db")
	// Write the database the way the pre-KV binary did, not through Open.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(preKVSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	oldMinute := time.Now().Truncate(time.Minute).Add(-time.Hour).Unix()
	if _, err := db.Exec(`INSERT INTO samples_1m
		(minute, gpus, holders, squat_avg, waiting_max, cpu_avg, ram_avg, decode, rpm_avg, p95_max, n)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		oldMinute, "[]", "{}", 0, 0, 0, 0, "{}", 0, 0, 1); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('schema', '1'), ('created', '2026-01-01T00:00:00Z')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The new binary opens the very same file.
	s, err := Open(path, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("pre-KV database would not open: %v", err)
	}
	defer s.Close()
	for _, table := range []string{"samples_1m", "samples_1h"} {
		var have int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('` + table + `') WHERE name = 'kv'`).Scan(&have); err != nil {
			t.Fatal(err)
		}
		if have != 1 {
			t.Fatalf("%s kv column: have %d, want 1", table, have)
		}
	}
	var kv string
	if err := s.db.QueryRow(`SELECT kv FROM samples_1m WHERE minute = ?`, oldMinute).Scan(&kv); err != nil {
		t.Fatal(err)
	}
	if kv != "{}" {
		t.Fatalf("old row kv = %q, want '{}'", kv)
	}
	// New data lands beside the old rows, and both read back.
	now := time.Now()
	m0 := now.Truncate(time.Minute).Add(-2 * time.Minute)
	if err := s.IngestSamples([]proto.Sample{
		sampleAt(m0.Add(5*time.Second), 0),
		sampleAt(m0.Add(40*time.Second), 1),
		sampleAt(m0.Add(time.Minute+30*time.Second), 2),
		sampleAt(m0.Add(2*time.Minute+5*time.Second), 3), // closes both minutes
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune(now, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(2*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	var oldSeen, newSeen bool
	for _, sp := range h.Samples {
		if sp.T.Unix() == oldMinute {
			oldSeen = len(sp.KV) == 0
		}
		if sp.T.Unix() == m0.Unix() {
			newSeen = sp.KV["agent"].Pressure > 0
		}
	}
	if !oldSeen || !newSeen {
		t.Fatalf("history: old row with empty kv = %v, new row with kv = %v (samples=%d)", oldSeen, newSeen, len(h.Samples))
	}
	st, err := s.Storage()
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows["samples_1m"] != 3 {
		t.Fatalf("samples_1m rows = %d, want the old one plus the two folded", st.Rows["samples_1m"])
	}
}
