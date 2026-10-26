// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package park

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recording is one captured request, for asserting method, path, query
// and body per call.
type recording struct {
	Method, Path, Query, Body string
}

func newRecorder(t *testing.T) (*httptest.Server, *[]recording) {
	t.Helper()
	got := []recording{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, recording{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(b)})
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestForTable(t *testing.T) {
	for _, kind := range []string{"comfyui", "vllm", "sglang"} {
		a, ok := For(kind, "", "")
		if !ok || a.Park == nil || a.Resume == nil {
			t.Fatalf("For(%q) = %+v, %v: the table covers it", kind, a, ok)
		}
	}
	if _, ok := For("llamacpp", "", ""); ok {
		t.Fatal("For(llamacpp) found no adapter, as expected")
	}
	// A URL overrides the table on the side it is set on, per side.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	a, ok := For("comfyui", srv.URL+"/offload", "")
	if !ok || a.Park == nil || a.Resume == nil {
		t.Fatalf("For(comfyui, url, ) = %+v, %v", a, ok)
	}
	// A kind with no table entry needs the URLs for anything to exist.
	b, ok := For("custom", srv.URL, "")
	if !ok || b.Park == nil || b.Resume != nil {
		t.Fatalf("For(custom, url, ) = %+v, %v", b, ok)
	}
	if _, ok := For("custom", "", ""); ok {
		t.Fatal("For(custom) with no URLs must be unusable")
	}
}

func TestBuiltInAdapters(t *testing.T) {
	tests := []struct {
		kind      string
		park      recording // the request Park must send; empty = it sends none
		resume    recording // the request Resume must send; empty = it sends none
		resumeNil bool      // Resume must not touch the wire at all
	}{
		{kind: "comfyui",
			park:      recording{Method: "POST", Path: "/free", Body: `{"unload_models":true}`},
			resumeNil: true, // ComfyUI loads on the next prompt, so there is
			// no resume call to make
		},
		{kind: "vllm",
			park:   recording{Method: "POST", Path: "/sleep", Query: "level=1"},
			resume: recording{Method: "POST", Path: "/wake_up"},
		},
		{kind: "sglang",
			park:   recording{Method: "POST", Path: "/release_memory_occupation"},
			resume: recording{Method: "POST", Path: "/resume_memory_occupation"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.kind, func(t *testing.T) {
			srv, got := newRecorder(t)
			a, ok := For(tc.kind, "", "")
			if !ok {
				t.Fatalf("For(%s) = %v", tc.kind, ok)
			}
			ctx := context.Background()
			if err := a.Park(ctx, srv.URL); err != nil {
				t.Fatalf("Park: %v", err)
			}
			if tc.park == (recording{}) {
				if len(*got) != 0 {
					t.Fatalf("Park sent %v, wanted no request", *got)
				}
			} else if g := (*got)[0]; g != tc.park {
				t.Fatalf("Park sent %+v, want %+v", g, tc.park)
			}
			if err := a.Resume(ctx, srv.URL); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if tc.resumeNil {
				if len(*got) != 1 {
					t.Fatalf("Resume sent %v, wanted no request", (*got)[1:])
				}
				return
			}
			if tc.resume == (recording{}) {
				if len(*got) != 1 {
					t.Fatalf("Resume sent %v, wanted no request", (*got)[1:])
				}
				return
			}
			if len(*got) != 2 {
				t.Fatalf("the calls sent %v", *got)
			}
			if g := (*got)[1]; g != tc.resume {
				t.Fatalf("Resume sent %+v, want %+v", g, tc.resume)
			}
		})
	}
}

func TestCustomHookOverridesTable(t *testing.T) {
	srv, got := newRecorder(t)
	a, ok := For("vllm", srv.URL+"/off", srv.URL+"/on")
	if !ok {
		t.Fatal("a configured hook is usable")
	}
	if err := a.Park(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := a.Resume(context.Background(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if g := (*got)[0]; g.Path != "/off" || g.Method != "POST" {
		t.Errorf("park sent %+v, want POST /off", g)
	}
	if g := (*got)[1]; g.Path != "/on" || g.Method != "POST" {
		t.Errorf("resume sent %+v, want POST /on", g)
	}
	for _, g := range *got {
		if g.Path == "/sleep" || g.Path == "/wake_up" {
			t.Errorf("the table ran instead of the hook: %+v", g)
		}
	}
}

func TestNon2xxNamesTheEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wake_up" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	a, ok := For("vllm", "", "")
	if !ok {
		t.Fatal("vllm is in the table")
	}
	if err := a.Park(context.Background(), srv.URL); err != nil {
		t.Fatalf("Park: %v", err)
	}
	err := a.Resume(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "POST "+srv.URL+"/wake_up") {
		t.Fatalf("Resume = %v, want the endpoint named", err)
	}
}

// strataLike answers as Strata's /v1/load and /v1/unload do: 415 unless
// the request is JSON, 400 unless the body is an object, 409 while busy.
func strataLike(t *testing.T, busy *bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, `{"error":{"message":"send application/json"}}`, http.StatusUnsupportedMediaType)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":{"message":"send a JSON object"}}`, http.StatusBadRequest)
			return
		}
		if *busy {
			http.Error(w, `{"error":{"type":"model_busy"}}`, http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCustomHookSendsJSON(t *testing.T) {
	busy := false
	srv := strataLike(t, &busy)
	a, ok := For("llamacpp", srv.URL+"/v1/unload", srv.URL+"/v1/load")
	if !ok {
		t.Fatal("a configured hook is usable")
	}
	if err := a.Park(context.Background(), srv.URL); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := a.Resume(context.Background(), srv.URL); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	busy = true
	err := a.Park(context.Background(), srv.URL)
	if err == nil || err.Error() != "POST "+srv.URL+"/v1/unload: 409 Conflict" {
		t.Fatalf("busy Park = %v, want POST %s/v1/unload: 409 Conflict", err, srv.URL)
	}
}
