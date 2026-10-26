// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package park

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/httpauth"
)

// The Berth park protocol: the three endpoints any engine can serve on its
// own HTTP API to be parkable without a Berth-side adapter. The contract,
// timing and a minimal reference server are in contrib/PARK-PROTOCOL.md.
const (
	// StateHot: the weights are on the card and the engine serves.
	StateHot = "hot"
	// StateParking: a park was asked and the weights are not off the card
	// yet.
	StateParking = "parking"
	// StateParked: the weights are in system RAM; only the engine's
	// residual is on the card.
	StateParked = "parked"
	// StateResuming: a resume was asked and the weights are not on the
	// card yet.
	StateResuming = "resuming"
)

// The protocol's paths, relative to the engine's base URL.
const (
	pathPark   = "/berth/park"
	pathResume = "/berth/resume"
	pathState  = "/berth/state"
)

// probeTimeout bounds the state read: the probe doubles as auto-detection
// for a hook with no URLs, so it must not hold a drain up.
var probeTimeout = 3 * time.Second

// knownState says whether s is one of the protocol's four states.
func knownState(s string) bool {
	switch s {
	case StateHot, StateParking, StateParked, StateResuming:
		return true
	}
	return false
}

// ForProtocol is the adapter for an engine that speaks the Berth park
// protocol: it parks through POST {base}/berth/park and resumes through
// POST {base}/berth/resume, both with no body. The base is bound in; the
// base a call is given is not used, as with a configured hook URL.
func ForProtocol(base string) Adapter {
	b := strings.TrimRight(base, "/")
	return Adapter{
		Park:   func(ctx context.Context, _ string) error { return postTo(ctx, b+pathPark, "") },
		Resume: func(ctx context.Context, _ string) error { return postTo(ctx, b+pathResume, "") },
	}
}

// Probe asks one engine whether it speaks the protocol: a single GET of
// {base}/berth/state, true when it answers 200 with a state Berth knows.
// Any other answer, a 404, a 500, a body that is not a known state, is a
// no: an engine without the protocol is not broken, it drains as today.
// A network error or a timeout is an error, since it cannot be told from
// the engine being down.
func Probe(ctx context.Context, base string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+pathState, nil)
	if err != nil {
		return false, err
	}
	httpauth.Apply(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("ask %s: %w", base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return false, nil
	}
	var st struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return false, nil
	}
	return knownState(st.State), nil
}
