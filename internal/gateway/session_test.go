// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"slices"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/runner"
)

func TestRunArgvCarriesSessionPolicy(t *testing.T) {
	e := config.Engine{Name: "fixture", ExitComfyUIIdle: true, ExitComfyUIIdleAfter: config.Duration{Duration: time.Minute}}
	o := runner.OptionsFor(&e, runner.Over{FailClosed: true})
	args := runArgv(o)
	if !slices.Contains(args, "--exit-comfyui-idle") || !slices.Contains(args, "--fail-closed") {
		t.Fatalf("lost session policy: %q", args)
	}
	i := slices.Index(args, "--exit-comfyui-idle-after")
	if i < 0 || i+1 >= len(args) || args[i+1] != "1m0s" {
		t.Fatalf("lost idle deadline: %q", args)
	}
}
