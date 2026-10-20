// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPrefetchByName(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "prefetch", "Qwen3-8B-Q4_K_M", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "into the page cache") {
		t.Errorf("the report does not say what was asked for: %s", out)
	}
	// The model's file and the projector that goes with it are both asked for.
	if !strings.Contains(out, "Qwen3-8B-Q4_K_M.gguf") || !strings.Contains(out, "mmproj") {
		t.Errorf("the ask does not cover the model and its projector: %s", out)
	}
}

func TestPrefetchSplitModelAsksForEveryPart(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "prefetch", "part", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	for _, want := range []string{"part-00001-of-00002.gguf", "part-00002-of-00002.gguf"} {
		if !strings.Contains(out, want) {
			t.Errorf("the ask lacks %s: %s", want, out)
		}
	}
	if !strings.Contains(out, "2 files") {
		t.Errorf("the split model's two parts are not both counted: %s", out)
	}
}

func TestPrefetchUnknownModel(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, _, errOut := runCLI(t, "prefetch", "no-such-model", "--dir", dir)
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "no-such-model") {
		t.Errorf("the error does not name the model: %s", errOut)
	}
}

func TestPrefetchJSONNamesTheFilesAndBytes(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "prefetch", "Qwen3-8B-Q4_K_M", "--dir", dir, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	var doc struct {
		Status string   `json:"status"`
		Model  string   `json:"model"`
		Target string   `json:"target"`
		Files  []string `json:"files"`
		Bytes  uint64   `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if doc.Status != "ok" {
		t.Errorf("status = %q, want ok", doc.Status)
	}
	if doc.Target != "Qwen3-8B-Q4_K_M" {
		t.Errorf("target = %q, want the model's name", doc.Target)
	}
	if len(doc.Files) != 2 {
		t.Errorf("files = %v, want the model and its projector", doc.Files)
	}
	if doc.Bytes == 0 {
		t.Errorf("bytes = 0, want the size of the files asked for")
	}
}
