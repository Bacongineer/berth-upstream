package models

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func rowsOf(rs ...ServedRequest) RowsFunc {
	return func(since time.Time) ([]ServedRequest, error) {
		var out []ServedRequest
		for _, r := range rs {
			if r.T.Before(since) {
				continue
			}
			out = append(out, r)
		}
		return out, nil
	}
}

func TestUsageFoldsRows(t *testing.T) {
	now := time.Now().UTC()
	rows := rowsOf(
		ServedRequest{T: now.Add(-time.Hour), Engine: "llama-swap", Model: "Qwen3-8B", Prompt: 100, Output: 20},
		ServedRequest{T: now, Engine: "strata", Model: "qwen3-8b", Prompt: 50.6, Output: 0},
		ServedRequest{T: now.Add(-time.Minute), Engine: "llama-swap", Model: " nomic-embed ", Prompt: 7, Output: 0},
		ServedRequest{T: now, Engine: "llama-swap", Model: "   "}, // a request that named nobody
		ServedRequest{T: now, Engine: "llama-swap", Prompt: 9},    // no model field at all
	)
	u, err := Usage(rows, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 2 {
		t.Fatalf("usage has %d keys, want 2 (one blank name is nobody's): %+v", len(u), u)
	}
	q := u["qwen3-8b"]
	if q.Requests != 2 {
		t.Errorf("requests = %d, want 2: the id and its lowercase spelling are one model", q.Requests)
	}
	if q.PromptTokens != 151 || q.OutputTokens != 20 {
		t.Errorf("tokens = %d/%d, want 151/20 (0.6 of a token rounds, a zero adds nothing)",
			q.PromptTokens, q.OutputTokens)
	}
	if !q.LastUsed.Equal(now) {
		t.Errorf("last used = %v, want the newest row %v", q.LastUsed, now)
	}
	if len(q.Engines) != 2 {
		t.Errorf("engines = %v, want both that served it", q.Engines)
	}
	if len(q.Names) != 2 {
		t.Errorf("names = %v, want both spellings kept", q.Names)
	}
	if e := u["nomic-embed"]; e.Requests != 1 || e.PromptTokens != 7 {
		t.Errorf("embedding usage = %+v, want 1 request of 7 prompt tokens (trimmed name)", e)
	}

	// The window is the caller's, and a nil reader is no database rather than a
	// failure.
	window, err := Usage(rows, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if window["qwen3-8b"].Requests != 1 {
		t.Errorf("windowed requests = %d, want 1", window["qwen3-8b"].Requests)
	}
	if got, err := Usage(nil, time.Time{}); err != nil || len(got) != 0 {
		t.Errorf("Usage(nil) = %v, %v, want no usage and no error", got, err)
	}
	if _, err := Usage(func(time.Time) ([]ServedRequest, error) { return nil, errors.New("locked") }, time.Time{}); err == nil {
		t.Error("a database that will not read was swallowed")
	}
}

func TestMatchUsage(t *testing.T) {
	now := time.Now().UTC()
	ms := []Model{
		{Name: "Qwen3-8B-Q4_K_M", Path: "/models/Qwen3-8B-Q4_K_M.gguf"},
		{Name: "Qwen3-8B-Q8_0", Path: "/models/Qwen3-8B-Q8_0.gguf"},
		{Name: "nomic-embed-text-v1.5", Path: "/models/nomic-embed-text-v1.5.Q4_K_M.gguf"},
	}
	usage := map[string]ModelUsage{
		// Exact stem.
		"qwen3-8b-q4_k_m": {Requests: 3, PromptTokens: 30, OutputTokens: 6, LastUsed: now},
		// The llama-swap id loads the Q8 file: that settles which quant a name
		// the two of them both contain was meant for.
		"qwen3-8b-q8": {Names: []string{"Qwen3-8B-Q8"}, Requests: 5, PromptTokens: 50, LastUsed: now.Add(-time.Minute)},
		// Contained in the file's stem, with no id to settle it: the longest stem
		// that matches wins, because it is the more specific name.
		"nomic-embed": {Requests: 1, LastUsed: now.Add(-time.Hour)},
		// And a model that is not on this machine.
		"gpt-4o": {Requests: 9, PromptTokens: 900, LastUsed: now},
	}
	orphan := MatchUsage(ms, usage, map[string]string{"qwen3-8b-q8": "/models/Qwen3-8B-Q8_0.gguf"})

	q4 := ms[0].Usage
	if q4 == nil || q4.Requests != 3 {
		t.Fatalf("q4 usage = %+v, want its own 3 requests", ms[0].Usage)
	}
	q8 := ms[1].Usage
	if q8 == nil || q8.Requests != 5 {
		t.Fatalf("q8 usage = %+v, want the 5 requests its id served", ms[1].Usage)
	}
	if len(q8.Names) != 1 || q8.Names[0] != "Qwen3-8B-Q8" {
		t.Errorf("q8 names = %v, want the id it is served under", q8.Names)
	}
	if ms[2].Usage == nil || ms[2].Usage.Requests != 1 {
		t.Errorf("embed usage = %+v, want the looser match", ms[2].Usage)
	}
	if len(orphan) != 1 {
		t.Fatalf("orphans = %v, want only gpt-4o", orphan)
	}
	if orphan["gpt-4o"].Requests != 9 {
		t.Errorf("orphan = %+v, want its 9 requests", orphan["gpt-4o"])
	}
}

func TestMatchUsageOneFileTwoRoots(t *testing.T) {
	// The same quant of the same model in two directories: a name two files
	// share is not a guess, it is both of them, so neither is credited with a
	// request the other served.
	ms := []Model{
		{Name: "same-Q4", Path: "/a/same-Q4.gguf"},
		{Name: "same-Q4", Path: "/b/same-Q4.gguf"},
	}
	orphan := MatchUsage(ms, map[string]ModelUsage{"same-q4": {Requests: 4}}, nil)
	if len(orphan) != 0 {
		t.Fatalf("orphan = %v, want the file matched, not dropped", orphan)
	}
	if ms[0].Usage == nil && ms[1].Usage == nil {
		t.Fatal("neither copy was credited")
	}
}

func TestSwapIDs(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := `
models:
  Qwen3-8B:
    cmd: llama-server -m /models/Qwen3-8B-Q4_K_M.gguf --port 8080
  Strata-27B:
    cmd: llama-server --model=/models/big/27b.gguf --port 8081
  mmproj-only:
    cmd: llama-server --mmproj /models/mmproj.gguf --port 8082
  no-cmd:
    description: nothing to read
`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ids := SwapIDs(cfg, nil)
	// An entry that names only an mmproj loads no model, so it ties no id to no
	// file: the model file is what an id means.
	if len(ids) != 2 {
		t.Fatalf("ids = %v, want the two that name a model file", ids)
	}
	if ids["qwen3-8b"] != "/models/Qwen3-8B-Q4_K_M.gguf" {
		t.Errorf("qwen id = %q, want the -m path, lowercased key", ids["qwen3-8b"])
	}
	if ids["strata-27b"] != "/models/big/27b.gguf" {
		t.Errorf("strata id = %q, want the --model= path", ids["strata-27b"])
	}
	// A config that is not there, or is not YAML, is no ids and no drama.
	if got := SwapIDs(filepath.Join(dir, "nope.yaml"), nil); got != nil {
		t.Errorf("missing config = %v, want nil", got)
	}
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("\t- not: [yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := SwapIDs(bad, nil); got != nil {
		t.Errorf("unreadable config = %v, want nil", got)
	}
	if got := modelArg(`llama-server --model "/models/a b.gguf" --port 1`); got != "/models/a b.gguf" {
		t.Errorf("modelArg = %q, want the quoted path", got)
	}
}

func TestSamePath(t *testing.T) {
	good := [][2]string{
		{"/models/a.gguf", "/models/a.gguf"},
		{"/srv/library/models/a.gguf", "library/models/a.gguf"},
		{"/models/x/../a.gguf", "/models/a.gguf"},
	}
	for _, p := range good {
		if !samePath(p[0], p[1]) {
			t.Errorf("samePath(%q, %q) = false, want true", p[0], p[1])
		}
	}
	bad := [][2]string{
		{"/models/a.gguf", "/models/b/a.gguf"},
		{"/models/xa.gguf", "/models/a.gguf"},
		{"/models/a.gguf", "/models/a.gguf2"},
	}
	for _, p := range bad {
		if samePath(p[0], p[1]) {
			t.Errorf("samePath(%q, %q) = true, want false", p[0], p[1])
		}
	}
}

// TestSwapIDsExpandsMacros is the real config's shape: a cmd whose model path is
// written as ${ollama}/..., where ollama is a macro that is itself an
// environment variable. What Berth cannot resolve, it does not claim.
func TestSwapIDsExpandsMacros(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := `
macros:
  server: "${env.LLAMA_SERVER}"
  ollama: "${env.MODELS_DIR}"
  models: "${ollama}"
models:
  defiance-27b:
    cmd: >-
      ${server} -m ${ollama}/HauhauCS/Qwen3.8-27B-IQ4_XS.gguf
      --mmproj ${ollama}/HauhauCS/mmproj-BF16.gguf --port ${PORT}
  unresolved:
    cmd: llama-server -m ${nope}/x.gguf
`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// No environment: nothing resolves, so no ids.
	if got := SwapIDs(cfg, nil); got != nil {
		t.Errorf("ids = %v, want none while every path is unresolved", got)
	}
	t.Setenv("MODELS_DIR", "/srv/models")
	t.Setenv("LLAMA_SERVER", "/usr/local/bin/llama-server")
	ids := SwapIDs(cfg, nil)
	if len(ids) != 1 {
		t.Fatalf("ids = %v, want the one path that resolved through both macros", ids)
	}
	if ids["defiance-27b"] != "/srv/models/HauhauCS/Qwen3.8-27B-IQ4_XS.gguf" {
		t.Errorf("path = %q, want ${ollama} expanded from the environment", ids["defiance-27b"])
	}
}

// llama-swap's unit sets MODELS_DIR for llama-swap, not for Berth: the
// environment read from the running llama-swap resolves ${env.X} first.
func TestSwapIDsWithLlamaSwapEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := "macros:\n  ollama: \"${env.BERTH_TEST_MODELS}\"\nmodels:\n  chat:\n    cmd: llama-server -m ${ollama}/org/chat-Q4_K_M.gguf --port ${PORT}\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SwapIDs(cfg, nil); got != nil {
		t.Fatalf("unresolved env must give nothing, got %v", got)
	}
	got := SwapIDs(cfg, map[string]string{"BERTH_TEST_MODELS": "/models"})
	if got["chat"] != "/models/org/chat-Q4_K_M.gguf" {
		t.Fatalf("ids = %v", got)
	}
}

func TestModelArgPositionalGGUF(t *testing.T) {
	if got := modelArg("berth run --as x -- /opt/ninfer-serve /m/q-IQ4_XS.gguf --port 1"); got != "/m/q-IQ4_XS.gguf" {
		t.Fatalf("positional gguf = %q", got)
	}
	if got := modelArg("llama-server -m /m/a.gguf --mmproj /m/b.gguf"); got != "/m/a.gguf" {
		t.Fatalf("-m must win, got %q", got)
	}
}
