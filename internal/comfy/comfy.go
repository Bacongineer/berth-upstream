// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package comfy speaks the ComfyUI HTTP API calls Berth makes: the queue
// read, the model unload, and the VRAM stats. Callers own their timeouts
// through ctx.
package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Queue reads how many jobs the server is running and has queued.
func Queue(ctx context.Context, base string) (running, pending int, err error) {
	var q struct {
		Running []json.RawMessage `json:"queue_running"`
		Pending []json.RawMessage `json:"queue_pending"`
	}
	if err := get(ctx, base, "/queue", &q); err != nil {
		return 0, 0, err
	}
	return len(q.Running), len(q.Pending), nil
}

// VRAM reads device 0's free and total VRAM in bytes.
func VRAM(ctx context.Context, base string) (free, total uint64, err error) {
	var s struct {
		Devices []struct {
			VRAMFree  uint64 `json:"vram_free"`
			VRAMTotal uint64 `json:"vram_total"`
		} `json:"devices"`
	}
	if err := get(ctx, base, "/system_stats", &s); err != nil {
		return 0, 0, err
	}
	if len(s.Devices) == 0 {
		return 0, 0, errors.New("/system_stats: no devices")
	}
	return s.Devices[0].VRAMFree, s.Devices[0].VRAMTotal, nil
}

// Free asks the server to unload its models, and to free VRAM too when
// freeMemory is set. ComfyUI's /free applies at the job boundary, so it
// never cuts a running generation off; a non-2xx answer is a failure.
func Free(ctx context.Context, base string, freeMemory bool) error {
	b, err := json.Marshal(struct {
		UnloadModels bool `json:"unload_models"`
		FreeMemory   bool `json:"free_memory,omitempty"`
	}{UnloadModels: true, FreeMemory: freeMemory})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/free", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("POST /free: %s", resp.Status)
	}
	return nil
}

// get reads one endpoint's JSON object or array. Errors name the endpoint,
// so a log says which call went wrong.
func get(ctx context.Context, base, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", endpoint, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: %w", endpoint, err)
	}
	return nil
}
