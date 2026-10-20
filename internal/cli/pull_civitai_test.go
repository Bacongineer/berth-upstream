// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

var errEmptyRange = errors.New("empty range")

// fakeSiteFull is a CivitAI-shaped server: version API plus a file host.
func fakeSiteFull(t *testing.T, file []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/model-versions/456":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 456, "name": "v2.0", "type": "Lora",
				"model": map[string]any{"id": 123, "name": "Test Model", "metadata": map[string]any{"baseModel": "Flux.1"}},
				"files": []map[string]any{
					{"id": 1, "name": "test.safetensors", "size": len(file), "hash": sha256hex(file),
						"type": "Safetensors", "downloadUrl": srv.URL + "/files/test.safetensors"},
				},
			})
		case "/files/test.safetensors":
			start := int64(0)
			if rng := r.Header.Get("Range"); len(rng) > 6 && rng[:6] == "bytes=" {
				start, _ = parseRangeStart2(rng[6:])
				w.WriteHeader(http.StatusPartialContent)
			}
			_, _ = w.Write(file[start:])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func parseRangeStart2(s string) (int64, error) {
	var v int64
	if s == "" {
		return 0, errEmptyRange
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errEmptyRange
		}
		v = v*10 + int64(c-'0')
	}
	return v, nil
}

func TestPullCivitaiDryRun(t *testing.T) {
	home := isolate(t)
	_ = home
	srv := fakeSiteFull(t, []byte("weights-bytes"))
	t.Setenv("CIVITAI_ENDPOINT", srv.URL)
	dir := t.TempDir()

	code, out, errOut := runCLI(t, "pull", "civitai:456", "--dir", dir, "--dry-run", "--format", "json")
	if code != 0 {
		t.Fatalf("dry run: code %d\n%s%s", code, out, errOut)
	}
	var doc struct {
		Dir    string `json:"dir"`
		DryRun bool   `json:"dry_run"`
		Files  []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the json did not parse: %v\n%s", err, out)
	}
	if !doc.DryRun || len(doc.Files) != 1 || doc.Files[0].Path != "test.safetensors" {
		t.Errorf("json lost the plan: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "civitai")); !os.IsNotExist(err) {
		t.Error("a dry run must not create the destination")
	}
}

func TestPullCivitai(t *testing.T) {
	home := isolate(t)
	_ = home
	body := []byte("the-file-body")
	srv := fakeSiteFull(t, body)
	t.Setenv("CIVITAI_ENDPOINT", srv.URL)
	dir := t.TempDir()

	code, out, errOut := runCLI(t, "pull", "civitai:456", "--dir", dir, "--format", "json")
	if code != 0 {
		t.Fatalf("pull: code %d\n%s%s", code, out, errOut)
	}
	got, err := os.ReadFile(filepath.Join(dir, "civitai", "test-model", "test.safetensors"))
	if err != nil {
		t.Fatalf("the file did not land: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("file = %q, want %q", got, body)
	}
	// The manifest says what it is: a civitai pull, by model name and version id.
	m := readManifest(t, filepath.Join(dir, "civitai", "test-model"))
	if m.Source != "civitai" || m.Name != "Test Model" || m.Ref != "456" {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Files) != 1 || m.Files[0].SHA256 == "" {
		t.Errorf("manifest files = %+v", m.Files)
	}
	// Pulling the same version again changes nothing.
	code, _, errOut = runCLI(t, "pull", "civitai:456", "--dir", dir, "--format", "json")
	if code != 0 {
		t.Fatalf("re-pull: code %d\n%s", code, errOut)
	}
}

func TestPullCivitaiFloorRefusal(t *testing.T) {
	home := isolate(t)
	cfg := filepath.Join(home, "config", "berth", "berth.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	// A floor of 100%: the refusal comes before a byte is downloaded.
	if err := os.WriteFile(cfg, []byte("[library]\nfree_floor = 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := fakeSiteFull(t, []byte("weights"))
	t.Setenv("CIVITAI_ENDPOINT", srv.URL)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI(t, "pull", "civitai:456", "--dir", dir, "--format", "json")
	if code == 0 {
		t.Fatalf("the floor must refuse the pull\n%s%s", out, errOut)
	}
	if want := "under its floor"; !strings.Contains(out+errOut, want) {
		t.Errorf("the refusal lost the floor:\n%s%s", out, errOut)
	}
	if _, err := os.Stat(filepath.Join(dir, "civitai")); !os.IsNotExist(err) {
		t.Error("a refused pull must not touch the destination")
	}
}

func readManifest(t *testing.T, dir string) (m struct {
	Source string `json:"source"`
	Name   string `json:"name"`
	Ref    string `json:"ref"`
	Files  []struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".berth-manifest.json"))
	if err != nil {
		t.Fatalf("no manifest: %v", err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest did not parse: %v", err)
	}
	return m
}
