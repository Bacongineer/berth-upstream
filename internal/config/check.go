// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/T-Crypt/berth/internal/park"
	"github.com/T-Crypt/berth/internal/proto"
)

// Defaults of the file's own schema. The daemon and `berth config show` both
// read the resolved config, so each default exists once.
const (
	DefaultEvery        = 5 * time.Second
	DefaultJuggleWindow = 15 * time.Second
	DefaultClearPolls   = 3
)

// Resolve returns c with every default of the file's schema materialised, so
// no caller has to know which zero means "unset".
func Resolve(c Config) Config {
	if c.Engines.Every.Duration == 0 {
		c.Engines.Every.Duration = DefaultEvery
	}
	if c.Engines.Discover == nil {
		discover := true
		c.Engines.Discover = &discover
	}
	if c.Claims.Builtin == nil {
		on := true
		c.Claims.Builtin = &on
	}
	if c.Claims.Every.Duration == 0 {
		c.Claims.Every.Duration = 2 * time.Second
	}
	if c.Alerts.ClearPolls == nil {
		polls := DefaultClearPolls
		c.Alerts.ClearPolls = &polls
	}
	// A zero juggle window is a missing default; a negative one is a decision
	// (juggle handling off) and survives Resolve.
	if c.Priority.JuggleWindow.Duration == 0 {
		c.Priority.JuggleWindow.Duration = DefaultJuggleWindow
	}
	if c.Park.TTL.Duration == 0 {
		c.Park.TTL.Duration = DefaultParkTTL
	}
	return c
}

// Warning is a setting that works but is likely a mistake: `berth config
// check` prints it, `--strict` fails on it.
type Warning struct {
	Key     string // the TOML key, with the index where one helps
	Message string
}

// Check reads and validates path like Load does and additionally reports the
// settings that are likely mistakes. A missing file is the built-in defaults,
// not an error.
func Check(path string) (Config, []Warning, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if errors.Is(err, os.ErrNotExist) {
		return Resolve(Config{}), nil, nil
	}
	if err != nil {
		return c, nil, fmt.Errorf("%s: %w", path, err)
	}
	if un := md.Undecoded(); len(un) > 0 {
		return c, nil, fmt.Errorf("%s: unknown keys %v", path, un)
	}
	if err := c.Validate(); err != nil {
		return c, nil, fmt.Errorf("%s: %w", path, err)
	}
	c = Resolve(c)
	return c, warnings(&c), nil
}

// warnings lists the settings that are valid but likely mistakes, in a fixed
// order so two runs of the check read the same.
func warnings(c *Config) []Warning {
	var out []Warning
	if c.Engines.Discover != nil && !*c.Engines.Discover && len(c.Engine) == 0 {
		out = append(out, Warning{Key: "engines.discover",
			Message: "discovery is off and no engine is configured: berth will read nothing"})
	}
	if d := c.Engines.Every.Duration; d > 0 && d < time.Second {
		out = append(out, Warning{Key: "engines.every",
			Message: fmt.Sprintf("every engine is re-read every %s", d)})
	}
	seen := map[string]int{} // url -> the first engine that reads it
	for i, e := range c.Engine {
		url := strings.TrimRight(e.URL, "/")
		if j, dup := seen[url]; dup {
			out = append(out, Warning{Key: fmt.Sprintf("engine[%d].url", i),
				Message: fmt.Sprintf("reads the same url as engine[%d] %q: one server, two cards", j, c.Engine[j].Name)})
			continue
		}
		seen[url] = i
	}
	for i, e := range c.Engine {
		if e.Type == "comfyui" && e.DrainComfyUI == "" {
			out = append(out, Warning{Key: fmt.Sprintf("engine[%d]", i),
				Message: "a handoff stops ComfyUI immediately; drain_comfyui waits for the job boundary"})
		}
	}
	for i, e := range c.Engine {
		for _, u := range []struct {
			key string
			url string
		}{{"park_url", e.ParkURL}, {"resume_url", e.ResumeURL}} {
			if u.url != "" && e.Park != "hook" {
				out = append(out, Warning{Key: fmt.Sprintf("engine[%d].%s", i, u.key),
					Message: fmt.Sprintf("%s is set but park is not hook, so Berth never calls it", u.key)})
			}
		}
	}
	for i, e := range c.Engine {
		if e.Park == "hook" && e.ParkURL == "" && e.ResumeURL == "" && !slices.Contains(park.BuiltInKinds(), e.Type) {
			out = append(out, Warning{Key: fmt.Sprintf("engine[%d].park", i),
				Message: fmt.Sprintf("%s: park = hook with no park_url/resume_url; Berth uses the Berth park protocol if the engine answers GET /berth/state, otherwise this engine drains as today", e.Name)})
		}
	}
	if f := c.Park.FloorGiB; f != nil && *f == 0 {
		out = append(out, Warning{Key: "park.floor_gib",
			Message: "0 turns the RAM floor off: Berth never stops a parked engine when RAM runs low, so the kernel's OOM killer picks what dies"})
	}
	// PSI "some avg10" is a percentage, so a threshold of 100 or more is
	// one no reading passes.
	if p := c.Park.Pressure; p >= 100 {
		out = append(out, Warning{Key: "park.pressure",
			Message: fmt.Sprintf("%g is past what /proc/pressure/memory reports (0 to 100): Berth never stops a parked engine for memory pressure", p)})
	}
	for i, d := range c.Models.Dirs {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			out = append(out, Warning{Key: fmt.Sprintf("models.dirs[%d]", i),
				Message: fmt.Sprintf("%s is not a directory: berth models will find nothing there", d)})
		}
	}
	return out
}

// AlertsSet is true when any threshold in the table is watched, so a report
// can leave the table out when the operator set none.
func (c *Config) AlertsSet() bool {
	for _, t := range []proto.Threshold{
		c.Alerts.Queue, c.Alerts.TTFT, c.Alerts.TPOT, c.Alerts.P95, c.Alerts.Cache,
		c.Alerts.Reingest, c.Alerts.VRAM, c.Alerts.Temp, c.Alerts.Power, c.Alerts.KV,
	} {
		if t.Warn != 0 || t.Crit != 0 {
			return true
		}
	}
	return false
}
