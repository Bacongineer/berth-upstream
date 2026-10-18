package hf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type hub struct {
	files   map[string][]byte // path -> content
	commit  string
	corrupt string // a path served with one byte flipped
	ranges  atomic.Int32
	gets    atomic.Int32
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (h *hub) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/revision/main"):
			fmt.Fprintf(w, `{"id":"org/repo","sha":%q}`, h.commit)
		case strings.Contains(r.URL.Path, "/tree/"+h.commit):
			var out []map[string]any
			for p, b := range h.files {
				e := map[string]any{"type": "file", "path": p, "size": len(b), "oid": "gitsha"}
				if strings.HasSuffix(p, ".gguf") {
					e["lfs"] = map[string]any{"oid": sum(b), "size": len(b)}
				}
				out = append(out, e)
			}
			_ = json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/org/repo/resolve/"+h.commit+"/"):
			p := strings.TrimPrefix(r.URL.Path, "/org/repo/resolve/"+h.commit+"/")
			b, ok := h.files[p]
			if !ok {
				http.NotFound(w, r)
				return
			}
			h.gets.Add(1)
			if p == h.corrupt {
				b = append([]byte{}, b...)
				b[len(b)/2] ^= 0xff
			}
			if rg := r.Header.Get("Range"); rg != "" {
				h.ranges.Add(1)
				from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(b)-1, len(b)))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(b[from:])
				return
			}
			_, _ = w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func blob(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i*7)
	}
	return b
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"hf:unsloth/Qwen3-0.6B-GGUF":          {"unsloth/Qwen3-0.6B-GGUF", "", "main"},
		"hf:unsloth/Qwen3-0.6B-GGUF:Q4_K_M":   {"unsloth/Qwen3-0.6B-GGUF", "Q4_K_M", "main"},
		"unsloth/Qwen3-0.6B-GGUF:Q4_K_M@v2":   {"unsloth/Qwen3-0.6B-GGUF", "Q4_K_M", "v2"},
		"https://huggingface.co/org/m:*.gguf": {"org/m", "*.gguf", "main"},
	} {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "hf:", "hf:noslash", "hf:a/b/c", "hf:../x"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}

func TestSelect(t *testing.T) {
	files := []File{
		{Path: "M-Q4_K_M.gguf"}, {Path: "M-Q8_0.gguf"}, {Path: "mmproj-F16.gguf"},
		{Path: "Big-Q4_K_M-00001-of-00002.gguf"}, {Path: "README.md"},
	}
	got, err := Select(files, "q8_0")
	if err != nil || len(got) != 2 || got[0].Path != "M-Q8_0.gguf" || got[1].Path != "mmproj-F16.gguf" {
		t.Fatalf("quant select = %+v, %v", got, err)
	}
	if miss := Missing([]File{{Path: "Big-Q4_K_M-00001-of-00002.gguf"}}); len(miss) != 1 || miss[0] != "Big-Q4_K_M-00002-of-00002.gguf" {
		t.Fatalf("missing parts = %v", miss)
	}
	if got, _ := Select(files, "README.md"); len(got) != 1 {
		t.Fatalf("file select = %+v", got)
	}
	if _, err := Select(files, "IQ1_S"); err == nil || !strings.Contains(err.Error(), "M-Q4_K_M.gguf") {
		t.Fatalf("no match must list the GGUFs, got %v", err)
	}
}

func TestPullResumeVerify(t *testing.T) {
	h := &hub{commit: "abc123", files: map[string][]byte{"m-Q4_K_M.gguf": blob(300_000, 3), "README.md": []byte("hi")}}
	srv := h.server(t)
	c := &Client{Base: srv.URL}
	ctx := context.Background()
	commit, err := c.Commit(ctx, "org/repo", "main")
	if err != nil || commit != "abc123" {
		t.Fatalf("commit = %q, %v", commit, err)
	}
	tree, err := c.Tree(ctx, "org/repo", commit)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := Select(tree, "Q4_K_M")
	dir := t.TempDir()

	// A third of the file is already here from an interrupted pull.
	part := filepath.Join(dir, "m-Q4_K_M.gguf.part")
	if err := os.WriteFile(part, h.files["m-Q4_K_M.gguf"][:100_000], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(ctx, "org/repo", commit, f[0], dir, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	if h.ranges.Load() != 1 {
		t.Fatalf("resume did not send a Range request")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "m-Q4_K_M.gguf"))
	if sum(got) != sum(h.files["m-Q4_K_M.gguf"]) {
		t.Fatal("resumed file differs")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal(".part left behind")
	}

	// Already here and correct: no second download.
	before := h.gets.Load()
	if skipped, err := c.Download(ctx, "org/repo", commit, f[0], dir, nil); err != nil || !skipped || h.gets.Load() != before {
		t.Fatalf("second pull: skipped=%v err=%v gets %d->%d", skipped, err, before, h.gets.Load())
	}

	if err := WriteManifest(dir, Manifest{Repo: "org/repo", Commit: commit, Files: f}); err != nil {
		t.Fatal(err)
	}
	checks, err := Verify(dir, nil)
	if err != nil || len(checks) != 1 || checks[0].Status != "ok" {
		t.Fatalf("verify = %+v, %v", checks, err)
	}
	// Flip a byte on disk: verify must catch it.
	got[10] ^= 1
	_ = os.WriteFile(filepath.Join(dir, "m-Q4_K_M.gguf"), got, 0o644)
	if checks, _ := Verify(dir, nil); checks[0].Status != "hash" {
		t.Fatalf("tampered file verified: %+v", checks)
	}
}

func TestCorruptDownloadIsRejected(t *testing.T) {
	h := &hub{commit: "c1", files: map[string][]byte{"bad.gguf": blob(50_000, 9)}, corrupt: "bad.gguf"}
	srv := h.server(t)
	c := &Client{Base: srv.URL}
	f := File{Path: "bad.gguf", Size: 50_000, SHA256: sum(h.files["bad.gguf"])}
	dir := t.TempDir()
	_, err := c.Download(context.Background(), "org/repo", "c1", f, dir, nil)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("want ErrIntegrity, got %v", err)
	}
	if h.gets.Load() != 1 {
		t.Fatalf("an integrity failure must not be retried blindly: %d gets", h.gets.Load())
	}
	for _, p := range []string{"bad.gguf", "bad.gguf.part"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Fatalf("%s left on disk after a failed check", p)
		}
	}
}

func TestGatedRepoSaysWhy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := (&Client{Base: srv.URL}).Commit(context.Background(), "org/gated", "main")
	if err == nil || !strings.Contains(err.Error(), "HF_TOKEN") {
		t.Fatalf("want a token hint, got %v", err)
	}
}
