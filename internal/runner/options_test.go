// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"reflect"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
)

// OptionsFor carries the engine entry's settings, fills the defaults it
// leaves open, and defers every zero override to the entry.
func TestOptionsFor(t *testing.T) {
	e := &config.Engine{Name: "strata", Priority: "foreground", NeedsMiB: 8192,
		Start: []string{"python", "serve.py"}, DrainPost: "http://127.0.0.1:1/unload",
		DrainSlots: "http://127.0.0.1:2", DrainComfyUI: "http://127.0.0.1:3",
		DrainMax: config.Duration{Duration: time.Minute}, KeepComfyUI: true}

	o := OptionsFor(e, Over{Socket: "s", Group: "strata", Wait: 10 * time.Second, Argv: []string{"true"}})
	got := o
	want := Options{
		Socket: "s", Engine: "strata", Group: "strata", Priority: "foreground", Needs: 8192 << 20,
		Wait: 10 * time.Second, Grace: 30 * time.Second, DrainPost: e.DrainPost, DrainSlots: e.DrainSlots,
		DrainComfyUI: e.DrainComfyUI, DrainMax: e.DrainMax.Duration, KeepComfyUI: true, Argv: []string{"true"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entry settings = %+v, want %+v", got, want)
	}

	o = OptionsFor(&config.Engine{Name: "e"}, Over{Socket: "s"})
	if o.Priority != "interactive" || o.Grace != 30*time.Second || len(o.Argv) != 0 {
		t.Errorf("bare entry = %+v, want the shared defaults", o)
	}

	// A zero override defers to the entry, an unset one to the default.
	o = OptionsFor(&config.Engine{Name: "e", Start: []string{"x"}}, Over{Socket: "s", Grace: 5 * time.Second, Argv: nil})
	if o.Grace != 5*time.Second || o.Argv[0] != "x" {
		t.Errorf("overrides = %+v", o)
	}
}
