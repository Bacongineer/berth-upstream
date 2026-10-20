// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/store"
)

// writeGGUF is the smallest file the reader accepts: a header, three metadata
// pairs and one tensor. The models package owns the format's own tests; here a
// model just has to exist with a name, an arch and a window.

// library is a directory as this machine's really stores models: two quants of
// one model, its vision projector, a draft, an embedder and a split model.

func TestModelsListingGroupsCompanions(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "--dir", dir, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	var doc struct {
		Status     string `json:"status"`
		Count      int    `json:"count"`
		Companions int    `json:"companions"`
		Files      int    `json:"files"`
		Models     []struct {
			Name       string   `json:"name"`
			Title      string   `json:"title"`
			Role       string   `json:"role"`
			Companions []string `json:"companions"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if doc.Status != "ok" {
		t.Errorf("status = %q, want ok", doc.Status)
	}
	// Three rows: two quants and the embedder. The projector is counted, and the
	// split model is one row of two files.
	if doc.Count != 4 {
		t.Errorf("count = %d, want 4 rows (two quants, the embedder, the split model): %s", doc.Count, out)
	}
	if doc.Companions != 1 {
		t.Errorf("companions = %d, want the one projector", doc.Companions)
	}
	if doc.Files != 6 {
		t.Errorf("files = %d, want all six", doc.Files)
	}
	var names []string
	for _, m := range doc.Models {
		names = append(names, m.Name)
		if m.Name == "Qwen3-8B-mmproj-f16" {
			t.Errorf("the projector was listed instead of counted: %v", names)
		}
		if m.Title == "" && m.Name != "part" && m.Name != "Qwen3-8B-mmproj-f16" {
			t.Errorf("%s has no title: general.name belongs in title", m.Name)
		}
	}
	want := []string{"Qwen3-8B-Q4_K_M", "Qwen3-8B-Q8_0", "nomic-embed", "part"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v, want %v sorted by file stem", names, want)
	}
	if doc.Models[0].Role != "model" {
		t.Errorf("role = %q, want model", doc.Models[0].Role)
	}
}

func TestModelsHumanTableAligns(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	for _, col := range []string{"MODEL", "QUANT", "SIZE", "CACHED", "PARAMS", "CTX", "ARCH", "+COMP", "USED"} {
		if !strings.Contains(out, col) {
			t.Errorf("stdout %q lacks the %s column", out, col)
		}
	}
	// A long name must not stagger the columns: where the second column starts in
	// the header is where it starts in every row. That is what tabwriter is for,
	// and hand-padded fixed widths are what it replaced.
	field2 := func(line string) int {
		i := strings.IndexByte(line, ' ')
		if i < 0 {
			return -1
		}
		for i < len(line) && line[i] == ' ' {
			i++
		}
		return i
	}
	header, rows := -1, 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "MODEL") {
			header = field2(line)
			continue
		}
		if !strings.HasPrefix(line, "Qwen3-8B-") && !strings.HasPrefix(line, "nomic-embed") {
			continue
		}
		rows++
		if got := field2(line); got != header {
			t.Errorf("row %q starts its second column at %d, the header starts it at %d", line, got, header)
		}
	}
	if rows < 3 {
		t.Fatalf("found %d model rows in %q", rows, out)
	}
	if !strings.Contains(out, "companion file") {
		t.Errorf("stdout %q does not say a companion file was counted", out)
	}
	if !strings.Contains(out, "128 MiB") && !strings.Contains(out, "GiB") && !strings.Contains(out, "KiB") {
		t.Errorf("stdout %q has no readable size", out)
	}
}

func TestModelsAllListsEveryFile(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "--dir", dir, "--all", "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, `"role": "mmproj"`) {
		t.Errorf("--all did not list the projector: %s", out)
	}
	code, out, _ = runCLI(t, "models", "--dir", dir, "--all")
	if code != ExitOK {
		t.Fatalf("--all exit = %d", code)
	}
	for _, want := range []string{"Qwen3-8B-mmproj-f16", "/mmproj"} {
		if !strings.Contains(out, want) {
			t.Errorf("--all output %q lacks %q", out, want)
		}
	}
}

// harborDB writes a harbor database where this isolated HOME says it lives, with
// one request per model name given.
func harborDB(t *testing.T, engineModel ...string) {
	t.Helper()
	path := defaultHarborDB()
	st, err := store.Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC()
	reqs := make([]proto.Request, 0, len(engineModel))
	for i, m := range engineModel {
		reqs = append(reqs, proto.Request{
			ID: "r" + string(rune('a'+i)), Engine: "llama-swap", Model: m,
			T: now.Add(-time.Duration(i) * time.Hour), Prompt: 100, Output: 20,
		})
	}
	if _, err := st.IngestRequests(reqs); err != nil {
		t.Fatal(err)
	}
}

func TestModelsUsageColumn(t *testing.T) {
	isolate(t)
	dir := library(t)
	// Two requests for the Q4 file, one under its stem and one under a
	// llama-swap id that contains it, and one for a model that is not here.
	harborDB(t, "Qwen3-8B-Q4_K_M", "qwen3-8b-q4_k_m-chat", "gpt-4o")

	code, out, errOut := runCLI(t, "models", "--dir", dir, "--usage", "7d", "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	var doc struct {
		Since     string `json:"usage_since"`
		Unmatched []struct {
			Model    string `json:"model"`
			Requests int    `json:"requests"`
		} `json:"served but not on disk"`
		Models []struct {
			Name  string `json:"name"`
			Usage *struct {
				Requests int `json:"requests"`
			} `json:"usage"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if doc.Since != "1w" {
		t.Errorf("usage_since = %q, want the 7d window the flag named", doc.Since)
	}
	if len(doc.Unmatched) != 1 || doc.Unmatched[0].Model != "gpt-4o" {
		t.Fatalf("served but not on disk = %+v, want only gpt-4o", doc.Unmatched)
	}
	if doc.Unmatched[0].Requests != 1 {
		t.Errorf("gpt-4o requests = %d, want 1", doc.Unmatched[0].Requests)
	}
	var q4 int
	for _, m := range doc.Models {
		if m.Name == "Qwen3-8B-Q4_K_M" {
			if m.Usage == nil {
				t.Fatalf("the model the log served has no usage block: %+v", doc.Models)
			}
			q4 = m.Usage.Requests
		}
	}
	if q4 != 2 {
		t.Errorf("Qwen3-8B-Q4_K_M requests = %d, want 2 (its stem and a name containing it)", q4)
	}

	// The human table gains REQ and TOKENS only when a window was asked for.
	code, out, _ = runCLI(t, "models", "--dir", dir, "--usage", "7d")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "REQ") || !strings.Contains(out, "TOKENS") {
		t.Errorf("--usage did not add its columns: %s", out)
	}
	if !strings.Contains(out, "since") {
		t.Errorf("the window is not in the heading: %s", out)
	}
	if code, out, _ := runCLI(t, "models", "--dir", dir); code != ExitOK {
		t.Fatalf("exit = %d", code)
	} else if strings.Contains(out, "TOKENS") {
		t.Errorf("without --usage the token columns appeared anyway: %s", out)
	}
	if !strings.Contains(out, "never") {
		t.Errorf("a model the log never served should read never: %s", out)
	}
}

func TestModelsUsageNoDatabase(t *testing.T) {
	isolate(t)
	dir := library(t)
	// No harbor has ever run here: the listing still reads, and USED is never.
	if _, err := os.Stat(defaultHarborDB()); err == nil {
		t.Fatal("isolate left a harbor database behind")
	}
	code, out, errOut := runCLI(t, "models", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "never") {
		t.Errorf("stdout %q has no never column", out)
	}
}

func TestModelsBadWindow(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, _, errOut := runCLI(t, "models", "--dir", dir, "--usage", "soon")
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "--usage") {
		t.Errorf("stderr %q does not name the flag", errOut)
	}
}

func TestModelsShowUsageBlock(t *testing.T) {
	isolate(t)
	dir := library(t)
	harborDB(t, "Qwen3-8B-Q8_0")
	code, out, errOut := runCLI(t, "models", "show", "Qwen3-8B-Q8_0", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	for _, want := range []string{"usage:", "1 request", "last used"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
	// A model nobody served says so rather than printing an empty block.
	code, out, _ = runCLI(t, "models", "show", "nomic-embed", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "nothing in the harbor log") {
		t.Errorf("stdout %q does not say the model has never served", out)
	}
}

func TestOllamaName(t *testing.T) {
	cases := []struct {
		stem, quant, want string
	}{
		{"Qwen3.8-27B-IQ4_XS", "IQ4_XS", "qwen3.8-27b-iq4_xs:iq4_xs"},
		{"deep.think Qwen-7B", "Q4_K_M", "deep.think-qwen-7b:q4_k_m"},
		{"model", "", "model:latest"},
		{"***", "Q8_0", "model:q8_0"},
	}
	for _, tc := range cases {
		if got := ollamaName(tc.stem, tc.quant); got != tc.want {
			t.Errorf("ollamaName(%q, %q) = %q, want %q", tc.stem, tc.quant, got, tc.want)
		}
	}
}

func TestModelsExportDryRun(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "export", "ollama", "Qwen3-8B-Q4_K_M", "--dir", dir, "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	for _, want := range []string{"FROM " + filepath.Join(dir, "Qwen3-8B-Q4_K_M.gguf"), "PARAMETER num_ctx 32768",
		"ollama create qwen3-8b-q4_k_m:q4_k_m -f "} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
	if !strings.Contains(out, "nothing was run") {
		t.Errorf("--dry-run ran something: %s", out)
	}
	// The printed command has to be runnable: --dry-run keeps the file it names.
	file := ""
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "-f "); i >= 0 {
			file = strings.TrimSpace(line[i+3:])
			break
		}
	}
	if raw, err := os.ReadFile(file); err != nil || !strings.Contains(string(raw), "FROM ") {
		t.Errorf("--dry-run printed a command naming %q, which is not a readable Modelfile (%v)", file, err)
	}
	if strings.Contains(out, "TEMPLATE") {
		t.Errorf("a TEMPLATE was written; the GGUF's own chat template should be left alone: %s", out)
	}
	// The window is the model's when the model's is smaller than Ollama's ceiling.
	code, out, _ = runCLI(t, "models", "export", "ollama", "nomic-embed", "--dir", dir, "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "PARAMETER num_ctx 2048") {
		t.Errorf("stdout %q does not ask for the model's own window", out)
	}
	// --as renames it.
	if code, out, _ := runCLI(t, "models", "export", "ollama", "nomic-embed", "--dir", dir, "--as", "embed:local", "--dry-run"); code != ExitOK {
		t.Fatalf("--as exit = %d", code)
	} else if !strings.Contains(out, "ollama create embed:local -f ") {
		t.Errorf("--as was ignored: %s", out)
	}
}

func TestModelsExportSplitRefused(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	// A real split pair: both files say they are two of two.
	writeGGUF(t, filepath.Join(dir, "part-00001-of-00002.gguf"), "llama", "Deep Split", 4096, 15, 2)
	writeGGUF(t, filepath.Join(dir, "part-00002-of-00002.gguf"), "llama", "Deep Split", 4096, 15, 2)
	code, _, errOut := runCLI(t, "models", "export", "ollama", filepath.Join(dir, "part-00001-of-00002.gguf"), "--dry-run")
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "llama-gguf-split --merge") {
		t.Errorf("stderr %q does not name the tool that fixes it", errOut)
	}
}

func TestModelsExportRunsFakeOllama(t *testing.T) {
	home := isolate(t)
	dir := library(t)
	// A fake ollama on PATH: it records the arguments it was given. A real
	// ollama create copies a 40 GiB blob and is never run by a test.
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "calls.txt")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\necho 'ollama said ok'\n"
	if err := os.WriteFile(filepath.Join(bin, "ollama"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	code, out, errOut := runCLI(t, "models", "export", "ollama", "Qwen3-8B-Q4_K_M", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "ollama said ok") {
		t.Errorf("the engine's own output was not passed through: %s", out)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ollama was never called: %v", err)
	}
	calls := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(calls, "create qwen3-8b-q4_k_m:q4_k_m -f ") {
		t.Errorf("ollama got %q, want create <name> -f <Modelfile>", calls)
	}
	if !strings.Contains(calls, "Modelfile") {
		t.Errorf("ollama got %q, want a Modelfile path", calls)
	}
	// The file it was given is gone: Berth keeps nothing after a success.
	modelfile := strings.Fields(calls)[3]
	if _, err := os.Stat(modelfile); err == nil {
		t.Errorf("the Modelfile was left behind at %s", modelfile)
	}
}

func TestModelsExportWithoutOllama(t *testing.T) {
	home := isolate(t)
	dir := library(t)
	// An PATH with nothing on it: the command is printed and the exit says the
	// job was not done.
	t.Setenv("PATH", filepath.Join(home, "empty"))
	code, out, errOut := runCLI(t, "models", "export", "ollama", "Qwen3-8B-Q4_K_M", "--dir", dir)
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(out, "FROM "+filepath.Join(dir, "Qwen3-8B-Q4_K_M.gguf")) {
		t.Errorf("the Modelfile was not printed: %s", out)
	}
	if !strings.Contains(errOut, "ollama is not installed") {
		t.Errorf("stderr %q does not say why", errOut)
	}
}

func TestModelsExportUnknown(t *testing.T) {
	isolate(t)
	dir := library(t)
	for _, args := range [][]string{
		{"models", "export", "vllm", "x", "--dir", dir},
		{"models", "export", "ollama", "--dir", dir},
		{"models", "export", "ollama", "not-a-model-here", "--dir", dir},
	} {
		code, _, errOut := runCLI(t, args...)
		if code != ExitFail {
			t.Errorf("%v exit = %d, want %d (stderr %q)", args, code, ExitFail, errOut)
		}
	}
}

func TestParseWindow(t *testing.T) {
	good := map[string]time.Duration{
		"30d":  30 * 24 * time.Hour,
		"2w":   14 * 24 * time.Hour,
		"24h":  24 * time.Hour,
		"90m":  90 * time.Minute,
		"1h0m": time.Hour,
	}
	for s, want := range good {
		got, err := parseWindow(s)
		if err != nil {
			t.Errorf("parseWindow(%q): %v", s, err)
			continue
		}
		if got != want {
			t.Errorf("parseWindow(%q) = %v, want %v", s, got, want)
		}
		if humanWindow(got) == "" {
			t.Errorf("humanWindow(%v) is empty", got)
		}
	}
	for _, s := range []string{"", "soon", "30", "-1d", "0d", "d"} {
		if _, err := parseWindow(s); err == nil {
			t.Errorf("parseWindow(%q) was accepted", s)
		}
	}
	if got := humanWindow(30 * 24 * time.Hour); got != "30d" {
		t.Errorf("humanWindow = %q, want 30d", got)
	}
	if got := humanWindow(2 * 7 * 24 * time.Hour); got != "2w" {
		t.Errorf("humanWindow = %q, want 2w", got)
	}
}

func TestSwapConfigPath(t *testing.T) {
	cases := [][]string{
		{"llama-swap", "--config", "/etc/llama-swap/config.yaml"},
		{"llama-swap", "--config=/etc/llama-swap/config.yaml"},
		{"llama-swap", "-c", "/etc/llama-swap/config.yaml"},
	}
	for _, start := range cases {
		if got := swapConfigPath(start); got != "/etc/llama-swap/config.yaml" {
			t.Errorf("swapConfigPath(%v) = %q", start, got)
		}
	}
	if got := swapConfigPath([]string{"llama-swap"}); got != "" {
		t.Errorf("a bare start = %q, want nothing to read", got)
	}
}
