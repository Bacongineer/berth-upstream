// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// releaseServes points APIURL at a server that answers the GitHub
// releases/latest shape; hits counts the requests it got, and wantUA is
// the User-Agent every check must carry.
func releaseServes(t *testing.T, tag string, assets map[string]string, hits *atomic.Int64, wantUA string) {
	t.Helper()
	rel := release{TagName: tag, HTMLURL: "https://github.com/T-Crypt/berth/releases/tag/" + tag}
	for name, url := range assets {
		rel.Assets = append(rel.Assets, struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		}{name, url})
	}
	var body any = rel
	if tag == "" {
		body = struct {
			Message string `json:"message"`
		}{"no release here"}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if got := r.Header.Get("User-Agent"); got != wantUA {
			t.Errorf("User-Agent = %q, want %q", got, wantUA)
		}
		if tag == "" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	APIURL = srv.URL
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.3.1", "0.3.0", true},
		{"v0.3.1", "v0.3.0", true},
		{"v0.10.0", "v0.9.9", true}, // numbers, not digits
		{"v0.3.1", "v0.3.1", false},
		{"v0.3.0", "v0.3.1", false},
		{"v0.3.1", "v0.4.0", false},
		{"v0.3.1", "dev", false}, // a dev tree is past the release line
		{"dev", "v0.3.1", false},
		{"v0.3.1-rc1", "v0.3.0", false},                         // not a plain release: do not guess
		{"v0.3.0", "0.3.0-rc", true},                            // a release is after its own pre-release
		{"v0.3.0", "0.2.1-0.20261020143430-9d05fd7afcf2", true}, // a pseudo-version build sees the release
		{"v0.3.0", "0.3.0+linux", false},                        // build metadata does not count
		{"v0.3.0", "0.3.0", false},
		{"not-a-version", "v0.3.0", false},
	} {
		if got := Newer(tc.a, tc.b); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	var hits atomic.Int64
	releaseServes(t, "v0.3.1", map[string]string{"berth-linux-amd64": "asset-url"}, &hits, "berth/0.3.0")

	got, err := Check(context.Background(), "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("the check made %d requests, want exactly one GET", hits.Load())
	}
	if got.Latest != "v0.3.1" || got.URL == "" || !got.Newer {
		t.Errorf("result = %+v", got)
	}
	if got.At.IsZero() {
		t.Error("the result keeps no timestamp of the check")
	}
	if got.Assets["berth-linux-amd64"] != "asset-url" {
		t.Errorf("assets = %v", got.Assets)
	}
}

func TestCheckFails(t *testing.T) {
	var hits atomic.Int64
	releaseServes(t, "", nil, &hits, "berth/0.3.0") // 404, like an unknown repository
	if _, err := Check(context.Background(), "0.3.0"); err == nil {
		t.Fatal("a 404 release answer is an error, not a result")
	}
}

func TestCacheFreshness(t *testing.T) {
	var hits atomic.Int64
	releaseServes(t, "v0.3.1", nil, &hits, "berth/0.3.0")
	path := filepath.Join(t.TempDir(), "update.json")

	if _, err := Refresh(context.Background(), path, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("first refresh made %d requests, want one", hits.Load())
	}
	// A fresh cache serves without a new request.
	got, err := Refresh(context.Background(), path, "0.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("a second refresh within the day made %d requests, want none", hits.Load())
	}
	if !got.Newer {
		t.Error("the cached answer is still newer than 0.3.0")
	}
	// The same answer re-priced against the version now running: having
	// updated to 0.3.1, nothing is newer.
	got, err = Refresh(context.Background(), path, "0.3.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Newer {
		t.Error("0.3.1 is not behind 0.3.1; a stale 'newer' would linger for a day")
	}

	// An aged cache re-asks.
	r, _ := LoadCache(path)
	r.At = time.Now().Add(-CacheTTL - time.Hour)
	if err := StoreCache(path, r); err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(context.Background(), path, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("an aged cache made %d requests, want one", hits.Load()-1)
	}
}

func TestRefreshKeepsStaleCacheOnFailure(t *testing.T) {
	var hits atomic.Int64
	releaseServes(t, "v0.3.1", nil, &hits, "berth/0.3.0")
	path := filepath.Join(t.TempDir(), "update.json")
	if _, err := Refresh(context.Background(), path, "0.3.0"); err != nil {
		t.Fatal(err)
	}

	// Age the cache out, then take the network down.
	r, _ := LoadCache(path)
	r.At = time.Now().Add(-CacheTTL - time.Hour)
	if err := StoreCache(path, r); err != nil {
		t.Fatal(err)
	}
	APIURL = "http://127.0.0.1:1" // nothing listens there

	got, err := Refresh(context.Background(), path, "0.3.0")
	if err != nil {
		t.Fatalf("a dead network with a stale cache returns the last answer, got %v", err)
	}
	if got.Latest != "v0.3.1" {
		t.Errorf("stale cache = %+v", got)
	}

	// With no answer at all, the error is the error.
	os.Remove(path)
	if _, err := Refresh(context.Background(), path, "0.3.0"); err == nil {
		t.Fatal("a dead network with no cache is an error")
	}
}

func TestStoreAndLoadCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "update.json")
	in := Result{Latest: "v0.3.1", URL: "u", Newer: true, At: time.Now().UTC()}
	if err := StoreCache(path, in); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// NTFS carries no Unix mode bits; the mode is checked where they exist.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("the cache holds a check answer and is mode %o, want 0600", perm)
	}
	got, ok := LoadCache(path)
	if !ok {
		t.Fatal("a stored cache does not load")
	}
	if got.Latest != in.Latest || got.URL != in.URL || got.Newer != in.Newer || !got.At.Equal(in.At) {
		t.Errorf("round trip = %+v, want %+v", got, in)
	}
	if _, ok := LoadCache(filepath.Join(t.TempDir(), "none.json")); ok {
		t.Error("a missing cache file is not an answer")
	}
}

func TestVerify(t *testing.T) {
	const name = "berth-linux-amd64"
	dir := t.TempDir()
	// the download lands beside the binary as binary.new
	f := filepath.Join(dir, name+".new")
	if err := os.WriteFile(f, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256Of([]byte("binary"))

	cases := map[string]struct {
		sums string
		ok   bool
	}{
		"match":     {sum + "  berth-linux-amd64\n", true},
		"text mode": {sum + " *berth-linux-amd64\n", true},
		"wrong hash": {
			"0000000000000000000000000000000000000000000000000000000000000000  berth-linux-amd64\n",
			false,
		},
		"file missing from sums": {"0000  other-file\n", false},
		"empty sums":             {"", false},
	}
	for want, tc := range cases {
		err := Verify([]byte(tc.sums), name, f)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want %v", want, err, tc.ok)
		}
	}
}

// writeAll is the test server's write: it reports, it does not panic.
func writeAll(t *testing.T, w http.ResponseWriter, b []byte) {
	t.Helper()
	if _, err := w.Write(b); err != nil {
		t.Errorf("test server: %v", err)
	}
}

func sha256Of(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDownload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAll(t, w, []byte("the binary"))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "berth.new")
	if err := Download(context.Background(), srv.URL, path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "the binary" {
		t.Errorf("downloaded %q", b)
	}
	// A temp file must not survive beside it.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if e.Name() != "berth.new" {
			t.Errorf("leftover %s in the target's directory", e.Name())
		}
	}

	// A refused download changes nothing.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer dead.Close()
	if err := Download(context.Background(), dead.URL, path); err == nil {
		t.Error("a 404 download is an error")
	}
}

func TestReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "berth")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "berth.new")
	if err := os.WriteFile(fresh, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceBinary(fresh, target); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "new" {
		t.Errorf("target holds %q after the replace", b)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Errorf("the downloaded file survives the replace: %v", err)
	}
	if runtime.GOOS != "windows" {
		mode, err := os.Stat(target)
		if err == nil {
			if p := mode.Mode().Perm(); p&0o111 == 0 {
				t.Errorf("the replaced binary lost its execute bits: %o", p)
			}
		}
	}
}

// A command the operator types asks GitHub even when the kept answer is
// fresh: a "no newer release" kept from before a release must not answer
// berth update after it.
func TestFetchIgnoresAFreshCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	if err := StoreCache(path, Result{Latest: "v0.2.0", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	releaseServes(t, "v0.3.0", nil, &hits, "berth/0.3.0-rc")
	got, err := Fetch(context.Background(), path, "0.3.0-rc")
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || got.Latest != "v0.3.0" || !got.Newer {
		t.Fatalf("Fetch = %+v after %d asks, want v0.3.0 newer after one ask", got, hits.Load())
	}
	if kept, ok := LoadCache(path); !ok || kept.Latest != "v0.3.0" {
		t.Fatalf("kept answer = %+v, want the fresh one", kept)
	}
}
