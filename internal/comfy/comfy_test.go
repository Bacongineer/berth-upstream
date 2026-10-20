// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package comfy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// server spins up a stand-in ComfyUI answering path with body, recording
// what each request carried.
type server struct {
	mu      sync.Mutex
	bodies  []string
	handler func(w http.ResponseWriter, r *http.Request)
}

func (s *server) record(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, string(b))
	s.mu.Unlock()
	s.handler(w, r)
}

func (s *server) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies
}

func TestQueue(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		status           int
		wantRunning      int
		wantPending      int
		wantErrSubstring string
	}{
		{name: "empty queue", body: `{"queue_running":[],"queue_pending":[]}`},
		{name: "one running, two queued", body: `{"queue_running":[[1,"job"]],"queue_pending":[[2],[3]]}`, wantRunning: 1, wantPending: 2},
		{name: "server down", status: http.StatusInternalServerError, wantErrSubstring: "GET /queue: 500 "},
		{name: "not JSON", body: `<html>`, wantErrSubstring: "/queue: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &server{handler: func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				fmt.Fprint(w, tt.body)
			}}
			srv := httptest.NewServer(http.HandlerFunc(st.record))
			defer srv.Close()

			running, pending, err := Queue(context.Background(), srv.URL)
			if tt.wantErrSubstring != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstring) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErrSubstring)
				}
				return
			}
			if err != nil {
				t.Fatalf("Queue: %v", err)
			}
			if running != tt.wantRunning || pending != tt.wantPending {
				t.Fatalf("Queue = %d running, %d pending, want %d, %d", running, pending, tt.wantRunning, tt.wantPending)
			}
		})
	}
}

func TestFree(t *testing.T) {
	tests := []struct {
		name       string
		freeMemory bool
		status     int
		wantBody   string
		wantErr    string
	}{
		{name: "unload and free", freeMemory: true, wantBody: `{"unload_models":true,"free_memory":true}`},
		{name: "unload only", wantBody: `{"unload_models":true}`},
		{name: "server refuses", status: http.StatusInternalServerError, wantErr: "POST /free: 500 Internal Server Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &server{handler: func(w http.ResponseWriter, r *http.Request) {
				if tt.status != 0 {
					w.WriteHeader(tt.status)
				}
				fmt.Fprint(w, `{}`)
			}}
			srv := httptest.NewServer(http.HandlerFunc(st.record))
			defer srv.Close()

			err := Free(context.Background(), srv.URL, tt.freeMemory)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Free: %v", err)
			}
			bodies := st.requests()
			if len(bodies) != 1 || bodies[0] != tt.wantBody {
				t.Fatalf("POST body = %v, want one %q", bodies, tt.wantBody)
			}
		})
	}
}

func TestVRAM(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantFree  uint64
		wantTotal uint64
		wantErr   string
	}{
		{name: "one device", body: `{"devices":[{"vram_free":3,"vram_total":24}]}`, wantFree: 3, wantTotal: 24},
		{name: "the first device", body: `{"devices":[{"vram_free":3,"vram_total":24},{"vram_free":9,"vram_total":9}]}`, wantFree: 3, wantTotal: 24},
		{name: "no devices", body: `{"devices":[]}`, wantErr: "/system_stats: no devices"},
		{name: "not JSON", body: `<html>`, wantErr: "/system_stats: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			free, total, err := VRAM(context.Background(), srv.URL)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("VRAM: %v", err)
			}
			if free != tt.wantFree || total != tt.wantTotal {
				t.Fatalf("VRAM = %d of %d, want %d of %d", free, total, tt.wantFree, tt.wantTotal)
			}
		})
	}
}

// Callers own their timeouts: a server that never answers makes the call
// come back at the context's deadline, not hang.
func TestCallersOwnTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := Queue(ctx, srv.URL); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Queue err = %v, want a deadline hit", err)
	}
	if err := Free(ctx, srv.URL, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Free err = %v, want a deadline hit", err)
	}
}
