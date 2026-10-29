// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package harbor

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// The page's files carry one ETag for the build and must be revalidated, so
// a reload with the same build answers 304 with no body instead of sending
// the page's 300 KB again.
func TestStaticFilesRevalidate(t *testing.T) {
	h := &Harbor{Socket: filepath.Join(t.TempDir(), "none.sock"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/harbor.js")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	tag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || tag == "" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("first load: %s, ETag %q, Cache-Control %q", resp.Status, tag, resp.Header.Get("Cache-Control"))
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/harbor.js", nil)
	req.Header.Set("If-None-Match", tag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified || len(body) != 0 {
		t.Fatalf("reload: %s with %d bytes, want 304 and none", resp.Status, len(body))
	}
}
