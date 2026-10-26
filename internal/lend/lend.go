// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package lend asks a serving engine to give part of its memory on one card
// back for a while, and to take it back later, without stopping. An Adapter
// names those calls for one engine: the Berth lend protocol
// (contrib/LEND-PROTOCOL.md) covers any engine that serves /vram/lend,
// /vram/return and /vram/state, and Strata's /v1/vram is built in. The
// engine's word that it lent is not proof the memory is free: the caller
// waits for a driver read (docs/design.md, rule 1). Nothing here touches a
// GPU or a process.
package lend

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

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/httpauth"
)

// Ticket is one open lend as the engine reported it.
type Ticket struct {
	ID  string `json:"id"`
	GPU string `json:"gpu"` // PCI address in sysfs form, 0000:09:00.0
	MiB uint64 `json:"mib"` // what the engine says it gave, which can exceed the ask
}

// Ask is one lend request: give MiB on the card at GPU. FreeMiB is what the
// driver last read free on that card, for an engine that takes a free
// target instead of an amount.
type Ask struct {
	GPU     string
	MiB     uint64
	FreeMiB uint64
}

// Adapter is one engine's lend calls.
type Adapter struct {
	Lend   func(ctx context.Context, a Ask) (Ticket, error)
	Return func(ctx context.Context, t Ticket) error
	// Open lists what is lent now, so a re-attach can rebuild the ledger.
	Open func(ctx context.Context) ([]Ticket, error)
}

var (
	// ErrBusy: the engine cannot lend right now (a request runs). Nothing
	// was lent.
	ErrBusy = errors.New("the engine is busy")
	// ErrUnsupported: the engine cannot lend at all as it was started.
	ErrUnsupported = errors.New("the engine cannot lend")
	// ErrPending: the engine did not answer in time and may still lend.
	// Never grant on it; return the lend once Open lists it.
	ErrPending = errors.New("the lend may still land")
	// ErrStillInUse: the return was refused because the lent memory is
	// still in use. It stays lent; try again later.
	ErrStillInUse = errors.New("the lent memory is still in use")
)

// TooBigError: the engine can lend at most MaxMiB on that card now (0 when
// it did not say). Nothing was lent.
type TooBigError struct{ MaxMiB uint64 }

func (e *TooBigError) Error() string {
	if e.MaxMiB == 0 {
		return "the engine cannot lend that much"
	}
	return fmt.Sprintf("the engine can lend at most %d MiB", e.MaxMiB)
}

// For picks the lend calls for an engine of kind with a lend_url, or the
// built-in for kind when lendURL is empty. ok is false when there is none.
func For(kind, url, lendURL string) (Adapter, bool) {
	if lendURL != "" {
		return protocol(lendURL), true
	}
	if kind == "strata" && url != "" {
		return strata(url), true
	}
	return Adapter{}, false
}

// BuiltInKinds are the engine kinds that lend with no lend_url.
func BuiltInKinds() []string { return []string{"strata"} }

func protocol(base string) Adapter {
	b := strings.TrimRight(base, "/")
	return Adapter{
		Lend: func(ctx context.Context, a Ask) (Ticket, error) {
			var t Ticket
			var tooBig struct {
				MaxMiB uint64 `json:"max_mib"`
			}
			code, err := call(ctx, http.MethodPost, b+"/vram/lend", map[string]any{"gpu": a.GPU, "mib": a.MiB},
				map[int]any{http.StatusOK: &t, http.StatusConflict: &tooBig})
			switch {
			case err != nil:
				return Ticket{}, err
			case code == http.StatusConflict:
				return Ticket{}, &TooBigError{MaxMiB: tooBig.MaxMiB}
			case code == http.StatusServiceUnavailable:
				return Ticket{}, ErrPending
			case code != http.StatusOK:
				return Ticket{}, statusError("POST", b+"/vram/lend", code)
			}
			t.GPU = normal(t.GPU)
			return t, nil
		},
		Return: func(ctx context.Context, t Ticket) error {
			code, err := call(ctx, http.MethodPost, b+"/vram/return", map[string]string{"id": t.ID}, nil)
			switch {
			case err != nil:
				return err
			case code == http.StatusOK, code == http.StatusNotFound:
				return nil
			case code == http.StatusConflict:
				return ErrStillInUse
			}
			return statusError("POST", b+"/vram/return", code)
		},
		Open: func(ctx context.Context) ([]Ticket, error) {
			var st struct {
				Leases []Ticket `json:"leases"`
			}
			code, err := call(ctx, http.MethodGet, b+"/vram/state", nil, map[int]any{http.StatusOK: &st})
			if err != nil {
				return nil, err
			}
			if code != http.StatusOK {
				return nil, statusError("GET", b+"/vram/state", code)
			}
			for i := range st.Leases {
				st.Leases[i].GPU = normal(st.Leases[i].GPU)
			}
			return st.Leases, nil
		},
	}
}

// strataID names Strata's one lend: it has a single reserve, so at most
// one lend is open.
// CallTimeout is the window one lend or take-back call to an engine gets. The
// engine answers once it has let the memory go, and Strata first finishes the
// answer it is streaming. berthd waits this long for the answer before rule 4's
// release grace starts counting.
const CallTimeout = 60 * time.Second

const strataID = "reserve"

// strata drives Strata's POST /v1/vram, which takes a free-memory target
// for the card ("reserve_mib") and shrinks or grows the expert cache to
// meet it; null goes back to the reserve the engine started with.
func strata(base string) Adapter {
	u := strings.TrimRight(base, "/") + "/v1/vram"
	vram := func(ctx context.Context, reserve any) (uint64, error) {
		var free struct {
			FreeMiB uint64 `json:"vram_free_mib"`
		}
		code, err := call(ctx, http.MethodPost, u, map[string]any{"reserve_mib": reserve}, map[int]any{http.StatusOK: &free})
		switch {
		case err != nil:
			return 0, err
		case code == http.StatusConflict:
			return 0, ErrBusy
		case code == http.StatusBadRequest:
			return 0, ErrUnsupported
		case code != http.StatusOK:
			return 0, statusError("POST", u, code)
		}
		return free.FreeMiB, nil
	}
	return Adapter{
		Lend: func(ctx context.Context, a Ask) (Ticket, error) {
			after, err := vram(ctx, a.FreeMiB+a.MiB)
			if err != nil {
				return Ticket{}, err
			}
			return Ticket{ID: strataID, GPU: normal(a.GPU), MiB: sub(after, a.FreeMiB)}, nil
		},
		Return: func(ctx context.Context, _ Ticket) error {
			_, err := vram(ctx, nil)
			if errors.Is(err, ErrBusy) {
				return ErrStillInUse
			}
			return err
		},
		// Strata keeps no ledger of lends: the caller's own record is the
		// only one, and a return with no lend open is harmless.
		Open: func(context.Context) ([]Ticket, error) { return nil, nil },
	}
}

// call sends body as JSON with the engine's key, and decodes the answer
// into into[status] when that status has a target.
func call(ctx context.Context, method, url string, body any, into map[int]any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpauth.Apply(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if v, ok := into[resp.StatusCode]; ok {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return 0, fmt.Errorf("%s %s: %s with a body that is not JSON: %w", method, url, resp.Status, err)
		}
		return resp.StatusCode, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func statusError(method, url string, code int) error {
	return fmt.Errorf("%s %s: %d %s", method, url, code, http.StatusText(code))
}

// normal is a PCI address in sysfs form, or the input when it is not one.
func normal(addr string) string {
	if a := gpu.SysfsAddr(addr); a != "" {
		return a
	}
	return addr
}

func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}
