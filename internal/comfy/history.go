// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package comfy

import (
	"context"
	"encoding/json"
	"errors"
)

// History reads the newest completed record instead of the entire job history.
func History(ctx context.Context, base string) (map[string]json.RawMessage, error) {
	var history map[string]json.RawMessage
	if err := get(ctx, base, "/history?max_items=1", &history); err != nil {
		return nil, err
	}
	if history == nil {
		return nil, errors.New("/history: expected an object")
	}
	return history, nil
}
