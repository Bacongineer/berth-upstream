// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"slices"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/runner"
)

func TestRunArgvCarriesCredentialReference(t *testing.T) {
	e := config.Engine{Name: "fixture", APIKeyEnv: "FIXTURE_DRAIN_KEY"}
	o := runner.OptionsFor(&e, runner.Over{})
	o.APIKey = "fixture-do-not-copy-to-argv"
	args := runArgv(o)
	i := slices.Index(args, "--api-key-env")
	if i < 0 || i+1 >= len(args) || args[i+1] != e.APIKeyEnv {
		t.Fatalf("credential source missing from %q", args)
	}
	if strings.Contains(strings.Join(args, " "), o.APIKey) {
		t.Fatal("resolved credential entered process arguments")
	}
}
