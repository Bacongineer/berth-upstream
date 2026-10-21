// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestComfySessionRecoversCompletedJobFromUnavailableBaseline(t *testing.T) {
	for _, tc := range []struct {
		name      string
		event     string
		offset    time.Duration
		undated   bool
		busy      bool
		initialOK bool
		want      bool
	}{
		{name: "completed_during_startup", event: "execution_success", want: true},
		{name: "completed_before_first_history_read", event: "execution_success", initialOK: true, want: true},
		{name: "failed_job_is_finished_work", event: "execution_error", want: true},
		{name: "interrupted_job_is_finished_work", event: "execution_interrupted", want: true},
		{name: "prior_job_is_not_this_session", event: "execution_success", offset: -time.Hour},
		{name: "future_clock_is_not_proof", event: "execution_success", offset: time.Hour},
		{name: "undated_history_is_not_proof", undated: true},
		{name: "started_only_is_not_completion", event: "execution_start"},
		{name: "busy_queue_keeps_lease", event: "execution_success", busy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int32
			completed := time.Now().Add(tc.offset).UnixMilli()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/queue":
					if tc.busy {
						fmt.Fprint(w, `{"queue_running":[[0,"active"]],"queue_pending":[]}`)
					} else {
						fmt.Fprint(w, `{"queue_running":[],"queue_pending":[]}`)
					}
				case "/history":
					if reads.Add(1) == 1 && !tc.initialOK {
						http.Error(w, "history not ready", http.StatusServiceUnavailable)
					} else if tc.undated {
						fmt.Fprint(w, `{"record":{}}`)
					} else {
						fmt.Fprintf(w, `{"record":{"status":{"messages":[[%q,{"timestamp":%d}]]}}}`, tc.event, completed)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			done := make(chan struct{})
			defer func() {
				select {
				case <-done:
				default:
					close(done)
				}
			}()
			session := comfySession{granted: time.Now().Add(-time.Second)}
			say := func(format string, args ...any) {
				message := fmt.Sprintf(format, args...)
				if strings.Contains(message, "waiting for the first ComfyUI job") ||
					strings.Contains(message, "waiting for ComfyUI to finish") {
					close(done)
				}
			}
			got := session.wait(Options{DrainComfyUI: srv.URL, ExitComfyUIIdle: true,
				FailClosed: true, Grace: 10 * time.Millisecond, Retry: time.Millisecond}, done, say)
			if got != tc.want {
				t.Fatalf("session release = %v, want %v; baseline must distinguish completed session work from lifetime history", got, tc.want)
			}
		})
	}
}
