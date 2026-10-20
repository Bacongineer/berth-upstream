// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// The test library's files were just written and read, so the kernel holds
// their pages: the column shows a measured share and its tier, not a dash.
func TestModelsCacheColumnShowsATier(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "COOL") && !strings.Contains(out, "COLD") {
		t.Errorf("stdout %q shows no tier beside a measured share", out)
	}
}

func TestModelsNoCacheCheckSkipsTheColumn(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "--dir", dir, "--no-cache-check")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "CACHED") {
		t.Errorf("the CACHED column disappeared instead of showing nothing: %s", out)
	}
	if strings.Contains(out, "COOL") || strings.Contains(out, "COLD") {
		t.Errorf("a skipped check still shows a tier: %s", out)
	}
}

// The one-model view says the same: how much of the file sits in the page
// cache and the tier that share reads as.
func TestModelsShowReportsTheCache(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "show", "Qwen3-8B-Q4_K_M", "--dir", dir)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "in the page cache") {
		t.Errorf("show has no cache line: %s", out)
	}
	if !strings.Contains(out, "COOL") && !strings.Contains(out, "COLD") {
		t.Errorf("show names no tier: %s", out)
	}
}

func TestModelsCacheCheckInJSON(t *testing.T) {
	isolate(t)
	dir := library(t)
	code, out, errOut := runCLI(t, "models", "--dir", dir, "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	var doc struct {
		CacheChecked bool `json:"cache_checked"`
		Models       []struct {
			Name       string `json:"name"`
			CachedSize uint64 `json:"cached_size"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if !doc.CacheChecked {
		t.Errorf("cache_checked = false, want true when the check ran")
	}
	for _, m := range doc.Models {
		if m.CachedSize == 0 {
			t.Errorf("%s was not measured: the check ran, so its files have a size", m.Name)
		}
	}
	code, out, errOut = runCLI(t, "models", "--dir", dir, "--no-cache-check", "--format", "json")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, errOut)
	}
	var skipped struct {
		CacheChecked bool `json:"cache_checked"`
	}
	if err := json.Unmarshal([]byte(out), &skipped); err != nil {
		t.Fatalf("json %q: %v", out, err)
	}
	if skipped.CacheChecked {
		t.Errorf("cache_checked = true after --no-cache-check")
	}
}
