// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSlotTimeoutHonoursFailMode(t *testing.T) {
	for _, state := range []struct {
		name string
		code int
		body string
	}{
		{"busy", http.StatusOK, `[{"is_processing":true}]`},
		{"unreadable", http.StatusServiceUnavailable, `{}`},
	} {
		for _, failClosed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail_closed=%v", state.name, failClosed), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(state.code)
					fmt.Fprint(w, state.body)
				}))
				defer srv.Close()
				done := make(chan struct{})
				defer close(done)
				ok := slotBoundaryAt(Options{Engine: "timeout-contract", FailClosed: failClosed,
					Grace: 5 * time.Millisecond, DrainMax: 20 * time.Millisecond, Retry: time.Millisecond}, srv.URL, done, t.Logf)
				if ok == failClosed {
					t.Fatalf("slot timeout handoff=%v, want %v with FailClosed=%v", ok, !failClosed, failClosed)
				}
			})
		}
	}
}
