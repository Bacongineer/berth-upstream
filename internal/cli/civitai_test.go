// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCivitaiSearchCommand(t *testing.T) {
	t.Setenv("CIVITAI_ENDPOINT", "http://127.0.0.1:1")
	home := isolate(t)
	_ = home
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("query") != "flux lora" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":42,"name":"Flux Lora","type":"Lora","user":{"name":"c"},"metadata":{"baseModel":"Flux.1"},"stats":{"downloadCount":100,"rating":4.5}}],"count":1}`))
	}))
	defer srv.Close()
	t.Setenv("CIVITAI_ENDPOINT", srv.URL)

	code, out, errOut := runCLI(t, "civitai", "search", "flux lora", "--format", "json")
	if code != 0 {
		t.Fatalf("search: code %d\n%s%s", code, out, errOut)
	}
	var doc struct {
		Models []struct {
			ID        int64   `json:"id"`
			Downloads int64   `json:"downloads"`
			Rating    float64 `json:"rating"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the json did not parse: %v\n%s", err, out)
	}
	if len(doc.Models) != 1 || doc.Models[0].ID != 42 || doc.Models[0].Downloads != 100 || doc.Models[0].Rating != 4.5 {
		t.Errorf("json lost the model: %s", out)
	}
	code, out, errOut = runCLI(t, "civitai", "search", "flux lora")
	if code != 0 {
		t.Fatalf("human search: code %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "Flux Lora") || !strings.Contains(out, "Lora") {
		t.Errorf("human output lost the model:\n%s", out)
	}
	// No query is a validation error, not a silent empty search.
	if code, _, _ := runCLI(t, "civitai", "search"); code == 0 {
		t.Error("search without a query must fail")
	}
}

func TestCivitaiVersionsCommand(t *testing.T) {
	home := isolate(t)
	_ = home
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models/7/versions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":90,"name":"v3","createdAt":"2026-08-01T00:00:00Z"},{"id":50,"name":"v2","createdAt":"2026-06-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()
	t.Setenv("CIVITAI_ENDPOINT", srv.URL)

	code, out, errOut := runCLI(t, "civitai", "versions", "7", "--format", "json")
	if code != 0 {
		t.Fatalf("versions: code %d\n%s%s", code, out, errOut)
	}
	var doc struct {
		Versions []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"versions"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the json did not parse: %v\n%s", err, out)
	}
	if len(doc.Versions) != 2 || doc.Versions[0].ID != 90 || doc.Versions[1].Name != "v2" {
		t.Errorf("json lost a version: %s", out)
	}
	code, out, errOut = runCLI(t, "civitai", "versions", "7")
	if code != 0 || !strings.Contains(out, "v3") {
		t.Fatalf("human versions: code %d\n%s%s", code, out, errOut)
	}
	if code, _, _ := runCLI(t, "civitai", "versions", "abc"); code == 0 {
		t.Error("a non-number model id must fail")
	}
}
