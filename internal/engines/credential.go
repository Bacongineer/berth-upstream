// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package engines

import "context"

type credentialKey struct{}
type credential struct {
	key string
	err error
}

func withoutCredential(ctx context.Context) context.Context {
	return context.WithValue(ctx, credentialKey{}, credential{})
}
