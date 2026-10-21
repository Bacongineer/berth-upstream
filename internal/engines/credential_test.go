// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
)

func TestStrataCredentialFileKeepsHealthIndependent(t *testing.T) {
	var detail, unauthorized atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"loaded":true,"model":"fixture"}`)
			return
		}
		detail.Add(1)
		if r.Header.Get("Authorization") != "Bearer fixture-file-token" {
			unauthorized.Add(1)
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/slots":
			fmt.Fprint(w, `[{"is_processing":true}]`)
		case "/metrics":
			fmt.Fprint(w, `{"live":{"running":1}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("fixture-file-token"), 0600); err != nil {
		t.Fatal(err)
	}
	e := config.Engine{Name: "fixture", Type: "strata", URL: srv.URL, APIKeyFile: path}
	w := &Watcher{}
	got := w.read(context.Background(), e)
	if !got.Up || got.Error != "" || len(got.Slots) != 1 || detail.Load() != 2 || unauthorized.Load() != 0 {
		t.Fatalf("valid credential: %+v, detail=%d unauthorized=%d", got, detail.Load(), unauthorized.Load())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	detail.Store(0)
	got = w.read(context.Background(), e)
	if !got.Up || got.Error == "" || detail.Load() != 0 {
		t.Fatalf("credential error changed health or sent unauthenticated requests: up=%v err=%q detail=%d", got.Up, got.Error, detail.Load())
	}
}
