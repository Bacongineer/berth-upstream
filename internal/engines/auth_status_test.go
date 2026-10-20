// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
)

func TestUnslothAuthReportUsesHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantAuth bool
	}{
		{"unauthorized", http.StatusUnauthorized, "", true},
		{"forbidden", http.StatusForbidden, "", true},
		{"server error", http.StatusInternalServerError, "", false},
		{"missing endpoint", http.StatusNotFound, "", false},
		{"malformed model list", http.StatusOK, "not-json", false},
		{"valid model list", http.StatusOK, `{"data":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/api/health") {
					fmt.Fprint(w, `{"status":"healthy"}`)
					return
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			// Status-like digits in an engine's URL must not classify its error.
			e := config.Engine{Name: "fixture", Type: "unsloth", URL: srv.URL + "/401/403"}
			got := (&Watcher{}).read(context.Background(), e)
			if !got.Up {
				t.Fatalf("the open health probe answered, but the engine is down: %+v", got)
			}
			if auth := strings.Contains(got.Error, "API key needed"); auth != tc.wantAuth {
				t.Fatalf("auth report = %v, want %v (HTTP %d): %s", auth, tc.wantAuth, tc.status, got.Error)
			}
		})
	}
}
