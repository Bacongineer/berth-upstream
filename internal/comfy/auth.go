// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package comfy

import (
	"context"
	"net/http"
)

type bearerKey struct{}

// WithBearer carries an engine credential through request-scoped API calls.
func WithBearer(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, bearerKey{}, key)
}

func authorize(req *http.Request) {
	if key, _ := req.Context().Value(bearerKey{}).(string); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
