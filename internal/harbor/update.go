// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package harbor

// The "Update available" notice: the one contact the harbor makes on its
// own. While [update] check in berth.toml is on, which is the default, it
// asks the GitHub API at most once a day, one GET, keeps the answer in
// the state directory, and serves it on /v1/update. Off, the harbor never
// touches GitHub and the page shows nothing. No endpoint changes state.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/T-Crypt/berth/internal/update"
)

// updateInfo is the answer /v1/update gives: the state of the check and,
// while it is on, the cached answer behind it.
type updateInfo struct {
	Enabled  bool   `json:"enabled"` // the [update] check switch
	Checking bool   `json:"checking,omitempty"`
	Latest   string `json:"latest,omitempty"`
	URL      string `json:"url,omitempty"`
	Newer    bool   `json:"newer"`
	At       string `json:"at,omitempty"` // when the check was made, RFC 3339
}

func (h *Harbor) update(w http.ResponseWriter, r *http.Request) {
	info := updateInfo{Enabled: h.UpdateCheck}
	if h.UpdateCheck {
		if res, ok := update.LoadCache(h.UpdateCache); ok {
			info.Latest = res.Latest
			info.URL = res.URL
			// The answer is re-priced against the version this harbor
			// runs, so an update of the binary drops the notice at once.
			info.Newer = update.Newer(res.Latest, h.Version)
			info.At = res.At.UTC().Format(time.RFC3339)
		} else {
			info.Checking = true
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(info)
}

// RunUpdate keeps the cached answer fresh while the check is on: one GET
// when the cache is stale, at most one a day after. A failed check is
// retried in an hour, so a dead network cannot leave the header blind for
// a day. Off, it starts nothing.
func (h *Harbor) RunUpdate(ctx context.Context) {
	if !h.UpdateCheck || h.UpdateCache == "" {
		return
	}
	go func() {
		for {
			_, err := update.Refresh(ctx, h.UpdateCache, h.Version)
			wait := update.CacheTTL
			if err != nil && ctx.Err() == nil {
				h.Log.Warn("harbor: the update check failed, retrying in an hour", "err", err)
				wait = time.Hour
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}
