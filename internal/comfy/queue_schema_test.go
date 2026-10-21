// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package comfy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQueueRejectsUnknownState(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"queue_running":[],"queue_pending":null}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
		_, _, err := Queue(context.Background(), srv.URL)
		srv.Close()
		if err == nil {
			t.Fatalf("accepted unknown queue state: %s", body)
		}
	}
}
