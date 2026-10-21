// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/runner"
	"github.com/T-Crypt/berth/internal/swapcost"
)

// costTestEngines is the config the route tests run against; the book is
// seeded per test, not here.
func costTestEngines() []config.Engine {
	return []config.Engine{
		{Name: "strata", Type: "llamacpp", URL: "http://127.0.0.1:8080", Serves: []string{"router-strata"}},
		{Name: "comfyui", Type: "comfyui", URL: "http://127.0.0.1:8188"},
	}
}

func costTestGateway(t *testing.T) (*Gateway, *httptest.Server) {
	t.Helper()
	g := &Gateway{Key: testKey, Log: discard(), Engines: costTestEngines()}
	gw := httptest.NewServer(g.Handler())
	t.Cleanup(gw.Close)
	return g, gw
}

type estimateRow struct {
	Tier    string `json:"tier"`
	TookMs  int64  `json:"took_ms"`
	Samples int    `json:"samples"`
	Known   bool   `json:"known"`
}

type costBody struct {
	Engine    string        `json:"engine"`
	Estimates []estimateRow `json:"estimates"`
}

// The cost route answers one estimate per tier: a pair the book has
// measured comes back with its median, one it has not comes back
// known:false rather than a guess, and hot is always the free switch.
func TestCostRoute(t *testing.T) {
	g, gw := costTestGateway(t)
	g.costBook().Record(swapcost.Sample{Engine: "strata", Tier: swapcost.Warm, Took: 8 * time.Second, At: time.Now()})
	g.costBook().Record(swapcost.Sample{Engine: "strata", Tier: swapcost.Cool, Took: 45 * time.Second, At: time.Now()})

	cases := []struct {
		name string
		url  string
		want int
	}{
		{"measured engine", "/v1/berth/cost?engine=strata", http.StatusOK},
		{"unmeasured engine", "/v1/berth/cost?engine=comfyui", http.StatusOK},
		{"no engine", "/v1/berth/cost", http.StatusBadRequest},
		{"engine not in config", "/v1/berth/cost?engine=nope", http.StatusNotFound},
	}
	for _, c := range cases {
		resp := call(t, http.MethodGet, gw.URL+c.url, "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("%s: status %d: %s", c.name, resp.StatusCode, body)
		}
	}

	resp := call(t, http.MethodGet, gw.URL+"/v1/berth/cost?engine=strata", "")
	var got costBody
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := costBody{Engine: "strata", Estimates: []estimateRow{
		{Tier: "hot", TookMs: 0, Samples: 0, Known: true},
		{Tier: "warm", TookMs: 8000, Samples: 1, Known: true},
		{Tier: "cool", TookMs: 45000, Samples: 1, Known: true},
		{Tier: "cold", TookMs: 0, Samples: 0, Known: false},
	}}
	if len(got.Estimates) != len(want.Estimates) || got.Engine != want.Engine {
		t.Fatalf("cost = %+v, want %+v", got, want)
	}
	for i := range want.Estimates {
		if got.Estimates[i] != want.Estimates[i] {
			t.Fatalf("estimate %d = %+v, want %+v", i, got.Estimates[i], want.Estimates[i])
		}
	}

	resp = call(t, http.MethodGet, gw.URL+"/v1/berth/cost?engine=comfyui", "")
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, e := range got.Estimates[1:] {
		if e.Known || e.Samples != 0 {
			t.Fatalf("unmeasured %s = %+v, want known:false", e.Tier, e)
		}
	}
}

type planStep struct {
	Engine  string `json:"engine"`
	Tier    string `json:"tier"`
	TookMs  int64  `json:"took_ms"`
	Samples int    `json:"samples"`
	Known   bool   `json:"known"`
}

type planBody struct {
	Steps      []planStep `json:"steps"`
	TotalMs    int64      `json:"total_ms"`
	Unmeasured int        `json:"unmeasured"`
	Prefetch   []string   `json:"prefetch"`
	KeepParked []string   `json:"keep_parked"`
}

// The plan route prices a declared chain from the book: known steps sum
// into the total, unknown ones are counted, and the steps the chain
// comes back to are listed for keeping parked.
func TestPlanRoute(t *testing.T) {
	g, gw := costTestGateway(t)
	g.costBook().Record(swapcost.Sample{Engine: "strata", Tier: swapcost.Cool, Took: 45 * time.Second, At: time.Now()})

	resp := call(t, http.MethodPost, gw.URL+"/v1/berth/plan",
		`{"chain":[{"engine":"comfyui","tier":"cold"},{"engine":"strata","tier":"cool"},{"engine":"comfyui","tier":"cold"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got planBody
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := planBody{
		Steps: []planStep{
			{Engine: "comfyui", Tier: "cold", Known: false},
			{Engine: "strata", Tier: "cool", TookMs: 45000, Samples: 1, Known: true},
			{Engine: "comfyui", Tier: "cold", Known: false},
		},
		TotalMs:    45000,
		Unmeasured: 2,
		Prefetch:   []string{"strata", "comfyui", ""},
		KeepParked: []string{"comfyui"},
	}
	if got.TotalMs != want.TotalMs || got.Unmeasured != want.Unmeasured ||
		strings.Join(got.Prefetch, ",") != strings.Join(want.Prefetch, ",") ||
		strings.Join(got.KeepParked, ",") != strings.Join(want.KeepParked, ",") ||
		len(got.Steps) != len(want.Steps) {
		t.Fatalf("plan = %+v, want %+v", got, want)
	}
	for i := range want.Steps {
		if got.Steps[i] != want.Steps[i] {
			t.Fatalf("step %d = %+v, want %+v", i, got.Steps[i], want.Steps[i])
		}
	}

	// Every bad body names what is wrong.
	for _, c := range []struct{ body, says string }{
		{`{`, "JSON"},
		{`{}`, "chain is empty"},
		{`{"chain":[]}`, "chain is empty"},
		{`{"chain":[{"engine":"strata","tier":"frozen"}]}`, `tier \"frozen\"`},
		{`{"chain":[{"engine":"strata","tier":"cold"},{"engine":"nope","tier":"hot"}]}`, `engine \"nope\"`},
	} {
		resp := call(t, http.MethodPost, gw.URL+"/v1/berth/plan", c.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), c.says) {
			t.Errorf("body %s: status %d: %s", c.body, resp.StatusCode, body)
		}
	}
}

// The cost and plan routes sit behind the same key and Origin checks as
// the rest of /v1.
func TestCostPlanRoutesGuarded(t *testing.T) {
	_, gw := costTestGateway(t)
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/berth/cost?engine=strata", ""},
		{http.MethodPost, "/v1/berth/plan", `{"chain":[{"engine":"strata","tier":"cold"}]}`},
	}
	for _, r := range routes {
		for _, c := range []struct {
			header map[string]string
			want   int
		}{
			{nil, http.StatusUnauthorized},
			{map[string]string{"Authorization": "Bearer nope"}, http.StatusUnauthorized},
			{map[string]string{"Authorization": "Bearer " + testKey, "Origin": "https://evil.example"}, http.StatusForbidden},
		} {
			req, _ := http.NewRequest(r.method, gw.URL+r.path, strings.NewReader(r.body))
			for k, v := range c.header {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("%s %s %v: status %d, want %d", r.method, r.path, c.header, resp.StatusCode, c.want)
			}
		}
	}
}

// waitFor fails unless out holds want before the deadline.
func waitFor(t *testing.T, out *syncBuf, want, fail string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %s", fail, out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A start the gateway ran goes into the book exactly once, from the
// grant to the engine answering. The queue the start waits behind the
// foreground holder is not the engine's switch cost: the sample and the
// whole request differ by that queue, so elapsed minus took must show
// more than a second the gateway waited before the card was free.
func TestStartRecordsOneSampleFromGrantToAnswer(t *testing.T) {
	sock, _ := daemon(t)
	holderOut := &syncBuf{}
	holder := make(chan int, 1)
	go func() { // foreground beats the gateway's start: the start waits behind, no drain
		holder <- runner.Run(runner.Options{Socket: sock, Engine: "ninfer", Priority: "foreground",
			Argv: []string{"sleep", "2"}, Stderr: holderOut})
	}()
	waitFor(t, holderOut, "granted", "holder never granted")

	addr := freeAddr(t)
	t.Setenv("BERTH_GW_ENGINE", addr+",cost-model,3s")
	costPath := filepath.Join(t.TempDir(), "swapcost.json")
	g := &Gateway{Socket: sock, Ready: 15 * time.Second, Key: testKey, Log: discard(), CostFile: costPath,
		Engines: []config.Engine{{Name: "cost-engine", Type: "llamacpp", URL: "http://" + addr,
			Start: []string{os.Args[0]}, Serves: []string{"cost-model"}}}}
	gw := httptest.NewServer(g.Handler())
	defer gw.Close()

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(`{"model":"cost-model"}`))
	req.Header.Set("Authorization", "Bearer "+testKey)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	<-holder

	// The book on disk: exactly one sample, on the cold tier (no
	// prefetch list means the page cache places nothing), and a time
	// that is the switch alone.
	raw, err := os.ReadFile(costPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Samples map[string]map[string][]struct {
			Engine string
			Tier   string
			Took   time.Duration
		}
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatalf("book = %s", raw)
	}
	tiers := disk.Samples["cost-engine"]
	total := 0
	for _, ring := range tiers {
		total += len(ring)
	}
	if total != 1 || len(tiers["cold"]) != 1 {
		t.Fatalf("samples = %s, want exactly one on the cold tier", raw)
	}
	took := tiers["cold"][0].Took
	if took <= 0 {
		t.Fatalf("took = %s, want a positive measured switch", took)
	}
	if gap := elapsed - took; gap < time.Second {
		t.Fatalf("took = %s of the whole request (%s): the wait before the grant leaked in", took, elapsed)
	}

	// A second request is proxied: no new start, no new sample.
	resp = call(t, http.MethodPost, gw.URL+"/v1/chat/completions", `{"model":"cost-model"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second call: status %d", resp.StatusCode)
	}
	book, err := swapcost.NewBook(costPath)
	if err != nil {
		t.Fatal(err)
	}
	if es := book.Estimate("cost-engine", swapcost.Cold); es.Samples != 1 || !es.Known {
		t.Fatalf("estimate after the second call = %+v, want the one sample", es)
	}
}
