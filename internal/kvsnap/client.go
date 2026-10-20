// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package kvsnap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/engines"
)

// ErrUnsupported means the engine cannot save or restore a slot cache: it
// started without --slot-save-path, or its build has no slot actions. The
// snapshot flow degrades to a plain swap; the engine keeps serving.
var ErrUnsupported = errors.New("slot saving is not supported by this engine")

// Slot is one entry of an engine's /slots list.
type Slot struct {
	ID int
	// Tokens is how many prompt tokens the slot holds; 0 when the engine
	// does not say, which is how an empty slot reads.
	Tokens int
	// Busy is whether the slot is processing a request, by the same rule
	// the engine watcher uses: the llama.cpp state string or the NInfer
	// is_processing bool, either of them.
	Busy bool
}

// Result is what one save or restore did, in the engine's own numbers.
type Result struct {
	Tokens int
	Bytes  int64
	Took   time.Duration
}

// Slots reads the engine's /slots list. base is the engine's URL, with or
// without a trailing slash. An engine without the endpoint answers 404 or
// 501, which is a plain error the caller swallows.
func Slots(ctx context.Context, base string) ([]Slot, error) {
	url := host(base) + "/slots"
	b, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID            int    `json:"id"`
		State         string `json:"state"`
		IsProcessing  bool   `json:"is_processing"`
		NPromptTokens int    `json:"n_prompt_tokens"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	out := make([]Slot, 0, len(raw))
	for _, s := range raw {
		out = append(out, Slot{ID: s.ID, Tokens: s.NPromptTokens, Busy: engines.SlotBusy(s.State, s.IsProcessing)})
	}
	return out, nil
}

// Save asks the engine to write slot's prompt cache into its snapshot
// directory as name.
func Save(ctx context.Context, base string, slot int, name string) (Result, error) {
	r, err := action(ctx, base, slot, "save", name)
	if err != nil {
		return Result{}, err
	}
	return Result{Tokens: r.NSaved, Bytes: r.NWritten, Took: ms(r.Timings.SaveMS)}, nil
}

// Restore asks the engine to read slot's prompt cache back from name in
// its snapshot directory.
func Restore(ctx context.Context, base string, slot int, name string) (Result, error) {
	r, err := action(ctx, base, slot, "restore", name)
	if err != nil {
		return Result{}, err
	}
	return Result{Tokens: r.NRestored, Bytes: r.NRead, Took: ms(r.Timings.RestoreMS)}, nil
}

// action is the request both actions share: POST /slots/{id}?action=ACT
// with the file name, the llama.cpp error mapping on the answer, and the
// numbers the engine reports back.
func action(ctx context.Context, base string, slot int, act, name string) (actionReply, error) {
	var r actionReply
	url := fmt.Sprintf("%s/slots/%d?action=%s", host(base), slot, act)
	body, err := json.Marshal(struct {
		Name string `json:"filename"`
	}{name})
	if err != nil {
		return r, err
	}
	resp, err := post(ctx, url, body)
	if err != nil {
		return r, fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return r, fmt.Errorf("POST %s: %w", url, err)
	}
	if m := disabledMessage(b); m != "" {
		return r, fmt.Errorf("%w: %s", ErrUnsupported, m)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusBadRequest ||
			resp.StatusCode == http.StatusNotFound ||
			resp.StatusCode == http.StatusNotImplemented {
			return r, fmt.Errorf("%w: %s", ErrUnsupported, resp.Status)
		}
		return r, fmt.Errorf("POST %s: %s, %s", url, resp.Status, snippet(b))
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("POST %s: %w", url, err)
	}
	return r, nil
}

// actionReply is what save and restore each report back. Every field is
// zero on the action that does not fill it.
type actionReply struct {
	IDSlot    int    `json:"id_slot"`
	Filename  string `json:"filename"`
	NSaved    int    `json:"n_saved"`
	NWritten  int64  `json:"n_written"`
	NRestored int    `json:"n_restored"`
	NRead     int64  `json:"n_read"`
	Timings   struct {
		SaveMS    float64 `json:"save_ms"`
		RestoreMS float64 `json:"restore_ms"`
	} `json:"timings"`
}

// disabledMessage is the engine's error message when the body says slot
// saving is off, else "". The message sits under "error" in current
// builds and at the top level in older ones.
func disabledMessage(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	m := e.Error.Message
	if m == "" {
		m = e.Message
	}
	if strings.Contains(m, "does not support slots") {
		return m
	}
	return ""
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// host is base without a trailing slash.
func host(base string) string {
	return strings.TrimRight(base, "/")
}

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	return b, nil
}

func post(ctx context.Context, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func ms(v float64) time.Duration {
	return time.Duration(v * float64(time.Millisecond))
}

// snippet is the engine's error body, cut to what fits an error line.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
