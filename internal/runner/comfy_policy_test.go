// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComfyBoundaryTimeoutHonorsFailMode(t *testing.T) {
	for _, closed := range []bool{false, true} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
		done := make(chan struct{})
		ok := jobBoundary(Options{Engine: "fixture", DrainComfyUI: srv.URL, FailClosed: closed, DrainMax: 20 * time.Millisecond, Retry: time.Millisecond}, done, t.Logf)
		close(done)
		srv.Close()
		if ok == closed {
			t.Fatalf("handoff=%v with fail_closed=%v", ok, closed)
		}
	}
}
