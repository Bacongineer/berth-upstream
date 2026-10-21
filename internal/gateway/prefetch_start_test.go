// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
)

// A start asks for the engine's files on its way to the lease, and the ask
// is never part of the start's success: every file failing still leaves an
// engine that answers.
func TestStartPrefetchesTheEnginesFiles(t *testing.T) {
	sock, _ := daemon(t)
	dir := t.TempDir()
	for _, n := range []string{"one.gguf", "two.gguf"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	addr := freeAddr(t)
	t.Setenv("BERTH_GW_ENGINE", addr+",qwen3-8b")

	var (
		mu    sync.Mutex
		asked []string
	)
	e := config.Engine{Name: "llama-server", Type: "llamacpp", URL: "http://" + addr,
		Start: []string{os.Args[0]}, Serves: []string{"qwen3-8b"}, Prefetch: []string{dir}}
	g := &Gateway{Socket: sock, Ready: 15 * time.Second, Key: testKey, Log: discard(),
		Engines: []config.Engine{e},
		Prefetch: func(p string) error {
			mu.Lock()
			asked = append(asked, p)
			mu.Unlock()
			return errors.New("the disk said no")
		}}
	if err := g.ensure(context.Background(), &e); err != nil {
		t.Fatalf("a failing ask failed the start: %v", err)
	}
	want := []string{filepath.Join(dir, "one.gguf"), filepath.Join(dir, "two.gguf")}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := slices.Equal(asked, want)
		got := slices.Clone(asked)
		mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("asked %v, want %v", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
