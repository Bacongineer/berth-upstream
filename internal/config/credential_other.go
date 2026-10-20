// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package config

import (
	"fmt"
	"os"
)

func openCredential(path string) (*os.File, error) {
	return nil, fmt.Errorf("api_key_file requires Unix file permissions; use api_key_env on this platform")
}
