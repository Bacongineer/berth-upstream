// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"io"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/park"
)

// Over is what the caller of OptionsFor supplies itself: the values a
// flag carries on `berth run`, and the writers and windows an internal
// caller owns. The zero value defers to the engine's entry.
type Over struct {
	Socket     string
	Group      string
	Pinned     bool
	Wait       time.Duration
	Grace      time.Duration
	FailClosed bool
	Argv       []string
	Stderr     io.Writer
	Output     io.Writer
}

// OptionsFor is the options one run of a configured engine takes: every
// setting the engine's berth.toml entry carries, with the caller's
// overrides where a flag or the caller owns the value. The defaults the
// entry leaves open are the shared ones: interactive priority, a 30 s
// drain grace, the engine's own start command.
func OptionsFor(e *config.Engine, o Over) Options {
	prio := e.Priority
	if prio == "" {
		prio = "interactive"
	}
	grace := o.Grace
	if grace == 0 {
		grace = 30 * time.Second
	}
	argv := o.Argv
	if argv == nil {
		argv = e.Start
	}
	probe := "/v1/models"
	if e.Type == "comfyui" {
		probe = "/system_stats"
	}
	opts := Options{
		Socket: o.Socket, Engine: e.Name, Group: o.Group, Priority: prio,
		Needs: e.NeedsMiB << 20, Pinned: o.Pinned, Wait: o.Wait,
		DrainPost: e.DrainPost, DrainSlots: e.DrainSlots, DrainComfyUI: e.DrainComfyUI,
		DrainMax: e.DrainMax.Duration, MinTurn: e.MinTurn.Duration, Container: e.Container,
		Grace: grace, FailClosed: o.FailClosed,
		Argv: argv, Stderr: o.Stderr, Output: o.Output,
	}
	// park = "hook" hands the drain the engine's own offload calls; when
	// the entry carries no URLs either, the drain probes Berth's park
	// protocol on the engine's URL instead. keep_comfyui_up is the same
	// alias for a ComfyUI: its unload stays the park, with the base the
	// drain URL names.
	if e.Park == "hook" || e.KeepComfyUI {
		adapter, ok := park.For(e.Type, e.ParkURL, e.ResumeURL)
		base := e.URL
		if e.Park != "hook" {
			base = e.DrainComfyUI
		}
		if ok {
			opts.Park = &adapter
		} else {
			opts.Hook = true
		}
		opts.ParkBase, opts.ParkProbe = base, probe
	}
	return opts
}
