package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

func readCSV(t *testing.T, buf *bytes.Buffer) [][]string {
	t.Helper()
	recs, err := csv.NewReader(buf).ReadAll()
	if err != nil {
		t.Fatalf("reading back the CSV: %v", err)
	}
	return recs
}

func TestRequestsCSVRoundTrip(t *testing.T) {
	reqs := []proto.Request{
		{
			ID: "r1", Engine: "llama-swap", Model: "model-a",
			T: time.Unix(1700000000, 0).UTC(), Status: 200,
			Prompt: 100, Cache: 40, Output: 250, Draft: 200, DraftAcc: 150,
			PrefillTPS: 1234.5, DecodeTPS: 45.25, DurationMS: 3000.5, PrefillMS: 20,
		},
		// Zero draft figures: the engine did not report them, and a zero
		// would read as a reported zero, so both cells are empty.
		{
			ID: "r2", Engine: "ollama", Model: "model-b",
			T: time.Unix(1700000060, 0).UTC(), Status: 200,
			Prompt: 10, Cache: 0, Output: 5,
			PrefillTPS: 500, DecodeTPS: 20, DurationMS: 1000, PrefillMS: 5,
		},
		// An engine name with a space must survive the CSV quoting.
		{
			ID: "r3", Engine: "my engine", Model: "model c",
			T: time.Unix(1700000120, 0).UTC(), Status: 500,
			Prompt: 50, Cache: 25, Output: 10, Draft: 30, DraftAcc: 0.5,
			PrefillTPS: 100, DecodeTPS: 10, DurationMS: 500, PrefillMS: 2,
		},
	}

	var buf bytes.Buffer
	if err := RequestsCSV(reqs, &buf); err != nil {
		t.Fatalf("writing the CSV: %v", err)
	}
	recs := readCSV(t, &buf)

	wantHeader := []string{
		"t", "id", "engine", "model", "status",
		"prompt_tokens", "cache_tokens", "output_tokens", "draft_tokens", "draft_accepted",
		"prefill_tps", "decode_tps", "duration_ms", "prefill_ms",
	}
	if !reflect.DeepEqual(recs[0], wantHeader) {
		t.Errorf("header = %v, want %v", recs[0], wantHeader)
	}

	want := [][]string{
		{"2023-11-14T22:13:20Z", "r1", "llama-swap", "model-a", "200",
			"100", "40", "250", "200", "150", "1234.5", "45.25", "3000.5", "20"},
		{"2023-11-14T22:14:20Z", "r2", "ollama", "model-b", "200",
			"10", "0", "5", "", "", "500", "20", "1000", "5"},
		{"2023-11-14T22:15:20Z", "r3", "my engine", "model c", "500",
			"50", "25", "10", "30", "0.5", "100", "10", "500", "2"},
	}
	if len(recs) != len(want)+1 {
		t.Fatalf("got %d rows, want %d", len(recs), len(want)+1)
	}
	for i, w := range want {
		if !reflect.DeepEqual(recs[i+1], w) {
			t.Errorf("row %d = %v, want %v", i+1, recs[i+1], w)
		}
	}
	// The empty-draft rule, stated on the cells themselves.
	if recs[2][8] != "" || recs[2][9] != "" {
		t.Errorf("zero draft cells = %q %q, want empty", recs[2][8], recs[2][9])
	}
}

func TestSamplesCSVColumnMap(t *testing.T) {
	h := proto.History{
		Every: 60,
		Samples: []proto.Sample{
			{
				T: time.Unix(1700000000, 0).UTC(),
				// gpu 1 is absent from this sample: its block reads empty.
				GPUs:    []proto.GPUSample{{Index: 0, Used: 524288000, Util: 50, Temp: 60, Power: 100}},
				Holders: map[string]uint64{"agent": 262144000},
				Decode:  map[string]float64{"agent": 30},
				KV:      map[string]proto.KVStat{"agent": {Window: 4096, Pressure: 0.5, Models: map[string]int{"m1": 2048, "m2": 1024}}},
				CPU:     10, RAMUsed: 4294967296, Squat: 0, Waiting: 1, RPM: 5.5, P95MS: 12.25,
			},
			{
				T:       time.Unix(1700000060, 0).UTC(),
				GPUs:    []proto.GPUSample{{Index: 0, Used: 1048576000, Util: 80, Temp: 65, Power: 150}, {Index: 1, Used: 524288000, Util: 20, Temp: 45, Power: 60}},
				Holders: map[string]uint64{"agent": 262144000, "coder": 104857600},
				Decode:  map[string]float64{"agent": 30, "coder": 10},
				KV: map[string]proto.KVStat{
					"agent": {Window: 4096, Pressure: 0.5, Models: map[string]int{"m1": 2048, "m2": 1024}},
					"coder": {Window: 8192, Pressure: 0.25, Models: map[string]int{"m3": 4096}},
				},
				CPU: 20, RAMUsed: 8589934592, Squat: 1048576, Waiting: 2, RPM: 7, P95MS: 20,
			},
		},
	}

	var buf bytes.Buffer
	if err := SamplesCSV(h, &buf); err != nil {
		t.Fatalf("writing the CSV: %v", err)
	}
	recs := readCSV(t, &buf)

	// The column map is the run's sorted union: gpus 0,1; engines agent,coder.
	wantHeader := []string{
		"t", "cpu_pct", "ram_used_mib", "waiting", "squat_mib", "rpm", "p95_ms",
		"gpu0_used_mib", "gpu0_util_pct", "gpu0_temp_c", "gpu0_power_w",
		"gpu1_used_mib", "gpu1_util_pct", "gpu1_temp_c", "gpu1_power_w",
		"holder_agent_mib", "decode_agent_tps", "kv_agent_pressure", "kv_agent_window", "kv_agent_models",
		"holder_coder_mib", "decode_coder_tps", "kv_coder_pressure", "kv_coder_window", "kv_coder_models",
	}
	if !reflect.DeepEqual(recs[0], wantHeader) {
		t.Errorf("header = %v, want %v", recs[0], wantHeader)
	}

	wantRow1 := []string{
		"2023-11-14T22:13:20Z", "10", "4096.0", "1", "0.0", "5.5", "12.25",
		"500.0", "50", "60", "100",
		"", "", "", "", // gpu 1 absent from this sample
		"250.0", "30", "0.5", "4096", "m1:2048;m2:1024",
		"", "", "", "", "", // coder absent from this sample
	}
	wantRow2 := []string{
		"2023-11-14T22:14:20Z", "20", "8192.0", "2", "1.0", "7", "20",
		"1000.0", "80", "65", "150",
		"500.0", "20", "45", "60",
		"250.0", "30", "0.5", "4096", "m1:2048;m2:1024",
		"100.0", "10", "0.25", "8192", "m3:4096",
	}
	if len(recs) != 3 {
		t.Fatalf("got %d rows, want 3", len(recs))
	}
	for i, w := range [][]string{wantRow1, wantRow2} {
		if !reflect.DeepEqual(recs[i+1], w) {
			t.Errorf("row %d = %v, want %v", i+1, recs[i+1], w)
		}
	}
}

func TestSamplesCSVEmptyRange(t *testing.T) {
	var buf bytes.Buffer
	if err := SamplesCSV(proto.History{Every: 60, Samples: []proto.Sample{}}, &buf); err != nil {
		t.Fatalf("writing the CSV: %v", err)
	}
	recs := readCSV(t, &buf)
	want := [][]string{{"t", "cpu_pct", "ram_used_mib", "waiting", "squat_mib", "rpm", "p95_ms"}}
	if !reflect.DeepEqual(recs, want) {
		t.Errorf("empty range = %v, want %v", recs, want)
	}
}

func TestRequestsCSVEmptyRange(t *testing.T) {
	var buf bytes.Buffer
	if err := RequestsCSV([]proto.Request{}, &buf); err != nil {
		t.Fatalf("writing the CSV: %v", err)
	}
	recs := readCSV(t, &buf)
	want := [][]string{{
		"t", "id", "engine", "model", "status",
		"prompt_tokens", "cache_tokens", "output_tokens", "draft_tokens", "draft_accepted",
		"prefill_tps", "decode_tps", "duration_ms", "prefill_ms",
	}}
	if !reflect.DeepEqual(recs, want) {
		t.Errorf("empty range = %v, want %v", recs, want)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	reqs := []proto.Request{
		{
			ID: "r1", Engine: "llama-swap", Model: "model-a",
			T: time.Unix(1700000000, 0).UTC(), Status: 200,
			Prompt: 100, Cache: 40, Output: 250, Draft: 200, DraftAcc: 150,
			PrefillTPS: 1234.5, DecodeTPS: 45.25, DurationMS: 3000.5, PrefillMS: 20,
		},
		{
			ID: "r2", Engine: "ollama", Model: "model-b",
			T: time.Unix(1700000060, 0).UTC(), Status: 200,
			Prompt: 10, Cache: 0, Output: 5,
			PrefillTPS: 500, DecodeTPS: 20, DurationMS: 1000, PrefillMS: 5,
		},
	}
	h := proto.History{
		Every: 60,
		Samples: []proto.Sample{{
			T:       time.Unix(1700000000, 0).UTC(),
			GPUs:    []proto.GPUSample{{Index: 0, Used: 524288000, Util: 50, Temp: 60, Power: 100}},
			Holders: map[string]uint64{"agent": 262144000},
			Decode:  map[string]float64{"agent": 30},
			KV:      map[string]proto.KVStat{"agent": {Window: 4096, Pressure: 0.5, Models: map[string]int{"m1": 2048}}},
			CPU:     10, RAMUsed: 4294967296, Squat: 0, Waiting: 1, RPM: 5.5, P95MS: 12.25,
		}},
	}

	rj, err := json.Marshal(reqs)
	if err != nil {
		t.Fatalf("marshaling requests: %v", err)
	}
	var back []proto.Request
	if err := json.Unmarshal(rj, &back); err != nil {
		t.Fatalf("unmarshaling requests: %v", err)
	}
	if !reflect.DeepEqual(back, reqs) {
		t.Errorf("requests round trip = %+v, want %+v", back, reqs)
	}
	// The wire shape is pinned on the zero-draft request alone: zero draft
	// is absent from its JSON, not a zero on the wire.
	if !bytes.Contains(rj, []byte(`"prompt_tokens":10`)) {
		t.Errorf("requests JSON = %s, want it to carry \"prompt_tokens\":10", rj)
	}
	one, err := json.Marshal(reqs[1])
	if err != nil {
		t.Fatalf("marshaling the zero-draft request: %v", err)
	}
	if bytes.Contains(one, []byte(`"draft_tokens"`)) || bytes.Contains(one, []byte(`"draft_accepted"`)) {
		t.Errorf("zero-draft request JSON = %s, zero draft must not appear on the wire", one)
	}

	hj, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshaling history: %v", err)
	}
	var hback proto.History
	if err := json.Unmarshal(hj, &hback); err != nil {
		t.Fatalf("unmarshaling history: %v", err)
	}
	if !reflect.DeepEqual(hback, h) {
		t.Errorf("history round trip = %+v, want %+v", hback, h)
	}
	if !bytes.Contains(hj, []byte(`"every_seconds":60`)) {
		t.Errorf("history JSON = %s, want it to carry \"every_seconds\":60", hj)
	}

	// An empty range marshals to [], never null.
	ej, err := json.Marshal([]proto.Request{})
	if err != nil {
		t.Fatalf("marshaling empty requests: %v", err)
	}
	if string(ej) != "[]" {
		t.Errorf("empty requests JSON = %s, want []", ej)
	}
}
