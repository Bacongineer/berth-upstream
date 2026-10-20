// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
)

func TestStrataOptionalTelemetryKeepsHealthAuthoritative(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"malformed", http.StatusOK, "not-json"},
		{"unauthorized", http.StatusUnauthorized, ""},
		{"forbidden", http.StatusForbidden, ""},
		{"missing", http.StatusNotFound, ""},
		{"server error", http.StatusInternalServerError, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					fmt.Fprint(w, `{"loaded":true,"model":"fixture","max_context":8192}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			got := (&Watcher{}).read(context.Background(), config.Engine{Name: "fixture", Type: "strata", URL: srv.URL})
			if !got.Up || got.Error != "" || len(got.Models) != 1 || got.Window != 8192 {
				t.Fatalf("optional telemetry changed the health verdict: %+v", got)
			}
		})
	}
}

func TestStrataRunningUsesPresence(t *testing.T) {
	for _, tc := range []struct {
		name string
		live string
		want float64
	}{
		{"standalone", `{"state":"generating"}`, 1},
		{"parallel", `{"state":"generating","running":2}`, 2},
		{"explicit idle", `{"state":"generating","running":0}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/health":
					fmt.Fprint(w, `{"loaded":true}`)
				case "/slots":
					fmt.Fprint(w, `[]`)
				case "/metrics":
					fmt.Fprintf(w, `{"live":%s}`, tc.live)
				}
			}))
			defer srv.Close()
			got := (&Watcher{}).read(context.Background(), config.Engine{Name: "fixture", Type: "strata", URL: srv.URL})
			if !got.Up || got.Metrics.Processing != tc.want {
				t.Fatalf("processing=%v, want %v: %+v", got.Metrics.Processing, tc.want, got)
			}
		})
	}
}
