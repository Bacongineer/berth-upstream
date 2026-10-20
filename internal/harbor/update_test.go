// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/update"
)

func TestUpdateEndpointShape(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update.json")
	h := &Harbor{
		Socket:      filepath.Join(t.TempDir(), "none.sock"),
		Version:     "0.3.0",
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		UpdateCheck: true,
		UpdateCache: cache,
	}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	// Enabled, no answer yet: the first check is on its way.
	var info updateInfo
	if err := getJSON(srv.URL+"/v1/update", &info); err != nil {
		t.Fatal(err)
	}
	if !info.Enabled || !info.Checking || info.Latest != "" {
		t.Errorf("before the first check: %+v", info)
	}

	// The check lands in the cache; the endpoint serves it.
	if err := update.StoreCache(cache, update.Result{
		Latest: "v0.3.1",
		URL:    "https://github.com/T-Crypt/berth/releases/tag/v0.3.1",
		Newer:  true,
		At:     time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	var after updateInfo // a fresh struct: decode does not clear fields the reply omits
	if err := getJSON(srv.URL+"/v1/update", &after); err != nil {
		t.Fatal(err)
	}
	if !after.Enabled || after.Checking || !after.Newer ||
		after.Latest != "v0.3.1" || after.URL == "" || after.At == "" {
		t.Errorf("the cached answer: %+v", after)
	}
}

func TestUpdateEndpointRepricesAgainstTheHarborVersion(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update.json")
	// the check was made when this harbor ran 0.3.0
	if err := update.StoreCache(cache, update.Result{Latest: "v0.3.1", URL: "u", Newer: true, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// the binary has since been updated to 0.3.1
	h := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"), Version: "0.3.1",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), UpdateCheck: true, UpdateCache: cache}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	var info updateInfo
	if err := getJSON(srv.URL+"/v1/update", &info); err != nil {
		t.Fatal(err)
	}
	if info.Newer {
		t.Errorf("0.3.1 is not behind v0.3.1: %+v", info)
	}
	if !info.Enabled || info.Latest != "v0.3.1" {
		t.Errorf("the answer is still served, only re-priced: %+v", info)
	}
}

func TestUpdateDisabledMakesNoRequest(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()
	prev := update.APIURL
	update.APIURL = srv.URL
	t.Cleanup(func() { update.APIURL = prev })

	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// Off: the harbor starts nothing, and the endpoint says so.
	off := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"), Version: "0.3.0",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	off.RunUpdate(ctx)
	time.Sleep(50 * time.Millisecond) // plenty for a started goroutine to hit the server
	if hits.Load() != 0 {
		t.Fatalf("the update check is off and the harbor made %d requests", hits.Load())
	}
	srvOff := httptest.NewServer(off.Handler())
	defer srvOff.Close()
	var info updateInfo
	if err := getJSON(srvOff.URL+"/v1/update", &info); err != nil {
		t.Fatal(err)
	}
	if info.Enabled || info.Latest != "" {
		t.Errorf("disabled: %+v", info)
	}
	if hits.Load() != 0 {
		t.Fatalf("serving the endpoint made %d requests; it reads the cache only", hits.Load())
	}

	// On, with a cache older than the day: the background check asks once.
	cache := filepath.Join(t.TempDir(), "update.json")
	if err := update.StoreCache(cache, update.Result{Latest: "v0.3.0", At: time.Now().Add(-update.CacheTTL - time.Hour)}); err != nil {
		t.Fatal(err)
	}
	on := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"), Version: "0.3.0",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), UpdateCheck: true, UpdateCache: cache}
	on.RunUpdate(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() == 0 {
		t.Fatal("the update check is on and the cache is stale, but nothing was asked")
	}
	if got := hits.Load(); got > 1 {
		t.Fatalf("the stale cache cost %d GETs; the answer is one GET a day", got)
	}
}

func getJSON(url string, v any) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
