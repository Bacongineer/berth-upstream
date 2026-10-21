// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSlotsRejectMissingProcessingState(t *testing.T) {
	for _, body := range []string{`null`, `[{}]`, `[{"state":"unknown"}]`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			if _, err := slotsBusy(srv.URL, ""); err == nil {
				t.Fatalf("missing processing state was accepted as idle: %s", body)
			}
		})
	}
}
