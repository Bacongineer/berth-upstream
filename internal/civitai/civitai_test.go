package civitai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeSite is the site's API and a file host with Range support behind it.
type fakeSite struct {
	srv   *httptest.Server
	file  []byte
	files int
	down  bool
}

func (s *fakeSite) close() { s.srv.Close() }

func newFakeSite(t *testing.T, file []byte) *fakeSite {
	t.Helper()
	s := &fakeSite{file: file}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "" {
			http.Error(w, "query required", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{
					"id": 123, "name": "Flux Lora Pack", "type": "Lora",
					"user":     map[string]any{"name": "somecreator"},
					"metadata": map[string]any{"baseModel": "Flux.1"},
					"stats":    map[string]any{"downloadCount": 9000, "rating": 4.8},
				},
				{"id": 124, "name": "SDXL Checkpoint", "type": "Checkpoint",
					"metadata": map[string]any{"baseModel": "SDXL"}},
			},
			"count": 2,
		})
	})
	mux.HandleFunc("/api/v1/models/123/versions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{
				{"id": 456, "name": "v2.0", "createdAt": "2026-09-01T00:00:00Z"},
				{"id": 111, "name": "v1.0", "createdAt": "2026-05-01T00:00:00Z"},
			},
		})
	})
	mux.HandleFunc("/api/v1/model-versions/456", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 456, "name": "v2.0", "type": "Lora",
			"model": map[string]any{
				"id": 123, "name": "Flux Lora Pack",
				"metadata": map[string]any{"baseModel": "Flux.1"},
			},
			"triggerWords": "fluxstyle",
			"files": []map[string]any{
				{"id": 789, "name": "flux-lora-v2.safetensors", "size": len(s.file),
					"hash": sum(s.file), "type": "Safetensors",
					"downloadUrl": s.srv.URL + "/files/flux-lora-v2.safetensors"},
			},
		})
	})
	mux.HandleFunc("/files/flux-lora-v2.safetensors", func(w http.ResponseWriter, r *http.Request) {
		start := int64(0)
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			start, _ = fmtSscanfInt(rng[len("bytes="):])
		}
		if start > 0 {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(s.file[start:])
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func fmtSscanfInt(s string) (int64, error) {
	var v int64
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a digit: %c", c)
		}
		v = v*10 + int64(c-'0')
	}
	return v, nil
}

func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in        string
		versionID string
		modelID   string
	}{
		{"civitai:456", "456", ""},
		{"456", "456", ""},
		{"civitai:model:123", "", "123"},
		{"https://civitai.com/model-versions/456", "456", ""},
		{"https://civitai.com/models/123", "", "123"},
	} {
		got, err := ParseRef(tc.in)
		if err != nil {
			t.Errorf("ParseRef(%q): %v", tc.in, err)
			continue
		}
		if got.VersionID != tc.versionID || got.ModelID != tc.modelID {
			t.Errorf("ParseRef(%q) = %+v, want version %q model %q", tc.in, got, tc.versionID, tc.modelID)
		}
	}
	if _, err := ParseRef("civitai:abc"); err == nil {
		t.Error("a non-number must not parse")
	}
	if _, err := ParseRef("civitai:model:abc"); err == nil {
		t.Error("a model id must be a number")
	}
}

func TestSearch(t *testing.T) {
	s := newFakeSite(t, []byte("weights"))
	defer s.close()
	c := &Client{Base: s.srv.URL}
	got, err := c.Search(context.Background(), "flux lora", []string{"Lora"}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2", len(got))
	}
	if got[0].ID != 123 || got[0].Name != "Flux Lora Pack" || got[0].BaseModel != "Flux.1" ||
		got[0].Creator != "somecreator" || got[0].Downloads != 9000 || got[0].Rating != 4.8 {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Type != "Checkpoint" || got[1].BaseModel != "SDXL" {
		t.Errorf("second = %+v", got[1])
	}
}

func TestResolveRef(t *testing.T) {
	s := newFakeSite(t, []byte("weights"))
	defer s.close()
	c := &Client{Base: s.srv.URL}
	ctx := context.Background()
	v, err := c.ResolveRef(ctx, Ref{VersionID: "456"})
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if v.ID != 456 || v.ModelName != "Flux Lora Pack" || v.BaseModel != "Flux.1" || v.TriggerWords != "fluxstyle" {
		t.Errorf("version = %+v", v)
	}
	if len(v.Files) != 1 || v.Files[0].Name != "flux-lora-v2.safetensors" || v.Files[0].SHA256 == "" {
		t.Errorf("files = %+v", v.Files)
	}
	if v.Files[0].Download != s.srv.URL+"/files/flux-lora-v2.safetensors" {
		t.Errorf("download url = %q", v.Files[0].Download)
	}
	v, err = c.ResolveRef(ctx, Ref{ModelID: "123"})
	if err != nil || v.ID != 456 {
		t.Errorf("model ref = %+v, %v; want the latest version", v, err)
	}
	if _, err := c.ResolveRef(ctx, Ref{VersionID: "999"}); err == nil || !strings.Contains(err.Error(), "no such") {
		t.Errorf("missing version = %v, want the site's not found", err)
	}
}

func TestDownload(t *testing.T) {
	body := []byte("0123456789")
	s := newFakeSite(t, body)
	defer s.close()
	c := &Client{Base: s.srv.URL}
	ctx := context.Background()
	v, err := c.Version(ctx, 456)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := c.Download(ctx, v, v.Files[0], dir, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "flux-lora-v2.safetensors"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("file = %d bytes, %v; want the body", len(got), err)
	}
	// A second pull of the same file is skipped.
	if skipped, err := c.Download(ctx, v, v.Files[0], dir, nil); err != nil || !skipped {
		t.Errorf("second download: skipped=%v err=%v", skipped, err)
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Flux Lora Pack":  "flux-lora-pack",
		"  a--b_c. ":      "a-b-c",
		"###":             "model",
		"Checkpoint v2.0": "checkpoint-v2-0",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
