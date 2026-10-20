// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package park

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProtocolAdapter(t *testing.T) {
	srv, got := newRecorder(t)
	a := ForProtocol(srv.URL)
	ctx := context.Background()
	if err := a.Park(ctx, "not used"); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := a.Resume(ctx, "not used"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if g := (*got)[0]; g.Method != "POST" || g.Path != "/berth/park" || g.Body != "" {
		t.Errorf("park sent %+v, want POST /berth/park with no body", g)
	}
	if g := (*got)[1]; g.Method != "POST" || g.Path != "/berth/resume" || g.Body != "" {
		t.Errorf("resume sent %+v, want POST /berth/resume with no body", g)
	}

	// A base with a trailing slash lands on the same paths.
	srv2, got2 := newRecorder(t)
	b := ForProtocol(srv2.URL + "/")
	if err := b.Park(ctx, ""); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if g := (*got2)[0]; g.Path != "/berth/park" {
		t.Errorf("trailing slash: park sent %+v", g)
	}
}

func TestProtocolNon2xxNamesTheEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/berth/resume" {
			w.WriteHeader(http.StatusConflict)
			return
		}
	}))
	defer srv.Close()
	a := ForProtocol(srv.URL)
	if err := a.Park(context.Background(), ""); err != nil {
		t.Fatalf("Park: %v", err)
	}
	if err := a.Resume(context.Background(), ""); err == nil {
		t.Fatal("a 409 resume is an error")
	}
}

func TestProbe(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int // 0 = no route on the server
		want   bool
	}{
		{name: "hot", body: `{"state":"hot"}`, status: 200, want: true},
		{name: "parking", body: `{"state":"parking"}`, status: 200, want: true},
		{name: "parked", body: `{"state":"parked"}`, status: 200, want: true},
		{name: "resuming", body: `{"state":"resuming"}`, status: 200, want: true},
		{name: "unknown state", body: `{"state":"hibernating"}`, status: 200},
		{name: "no state key", body: `{}`, status: 200},
		{name: "not a state object", body: `"hot"`, status: 200},
		{name: "no route", status: 404},
		{name: "broken engine", status: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/berth/state" {
					t.Errorf("the probe asked %s, want GET /berth/state", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("the probe sent %s, want GET", r.Method)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			got, err := Probe(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("Probe = %v, want %v with no error", err, tc.want)
			}
			if got != tc.want {
				t.Fatalf("Probe = %v, want %v", got, tc.want)
			}
		})
	}

	// A closed port is an error, not a no: it cannot be told from the
	// engine being down.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	if got, err := Probe(context.Background(), deadURL); err == nil {
		t.Fatalf("Probe on a closed port = %v, want the network error", got)
	}

	// A server that answers slowly: the probe still stops at its timeout,
	// and what it gets is an error, not a no.
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer hang.Close()
	prev := probeTimeout
	probeTimeout = 50 * time.Millisecond
	defer func() { probeTimeout = prev }()
	done := make(chan struct {
		got bool
		err error
	})
	go func() {
		g, e := Probe(context.Background(), hang.URL)
		done <- struct {
			got bool
			err error
		}{g, e}
	}()
	select {
	case r := <-done:
		if r.err == nil {
			t.Fatalf("a hung state read = %v, want the timeout error", r.got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the probe did not stop at its timeout")
	}
}
