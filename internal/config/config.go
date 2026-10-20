// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package config reads berth.toml. The file is optional: without one, the
// daemon runs on its flags and watches no engines. Engines use the same
// [[engine]] shape as Speculum's config, so a Speculum user can copy theirs.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/T-Crypt/berth/internal/claims"
	"github.com/T-Crypt/berth/internal/park"
	"github.com/T-Crypt/berth/internal/proto"
)

// Engine is one inference engine this node reads metrics from. Reading is
// all it does: Berth never sends an engine a prompt.
type Engine struct {
	Name string `toml:"name" json:"name" yaml:"name"`
	Type string `toml:"type" json:"type" yaml:"type"` // one of Types
	// Kind is what a custom engine produces (llm, image, tts, stt, other), so a
	// voice or image server Berth has no reader for is tracked as one. Readers
	// with a built-in kind (comfyui: image, kokoro: tts) ignore it.
	Kind string `toml:"kind,omitempty" json:"kind,omitempty" yaml:"kind,omitempty"`
	URL  string `toml:"url" json:"url" yaml:"url"` // any host and port
	// APIKeyEnv names an environment variable holding a bearer token for
	// engines that want one; the token itself never goes in the file.
	APIKeyEnv string `toml:"api_key_env,omitempty" json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	// Custom engines: every path is optional. Health decides up/down (else
	// metrics or models do, else GET /); Models is an OpenAI-style list;
	// Slots is a llama.cpp-style /slots; Map names the metrics to read.
	Health  string            `toml:"health,omitempty" json:"health,omitempty" yaml:"health,omitempty"`
	Models  string            `toml:"models,omitempty" json:"models,omitempty" yaml:"models,omitempty"`
	Metrics string            `toml:"metrics,omitempty" json:"metrics,omitempty" yaml:"metrics,omitempty"` // path of Prometheus text; custom default /metrics when Map is set
	Slots   string            `toml:"slots,omitempty" json:"slots,omitempty" yaml:"slots,omitempty"`
	Map     map[string]string `toml:"map,omitempty" json:"map,omitempty" yaml:"map,omitempty"` // field -> metric name
	// Gateway: how `berth gateway` starts this engine when a request names a
	// model it serves and it is not running. Start is the engine's command;
	// it runs under a lease exactly as `berth run` would.
	Start    []string `toml:"start,omitempty" json:"start,omitempty" yaml:"start,omitempty"`
	Serves   []string `toml:"serves,omitempty" json:"serves,omitempty" yaml:"serves,omitempty"`         // model names this engine answers for
	Priority string   `toml:"priority,omitempty" json:"priority,omitempty" yaml:"priority,omitempty"`   // batch, interactive (default) or foreground
	NeedsMiB uint64   `toml:"needs_mib,omitzero" json:"needs_mib,omitempty" yaml:"needs_mib,omitempty"` // 0 = the whole card
	// NeedsRAMMiB estimates the engine's host RAM spike at cold start, for
	// the gateway headroom guard. 0 = unknown, and the guard fails open.
	NeedsRAMMiB  uint64 `toml:"needs_ram_mib,omitzero" json:"needs_ram_mib,omitempty" yaml:"needs_ram_mib,omitempty"`
	DrainPost    string `toml:"drain_post,omitempty" json:"drain_post,omitempty" yaml:"drain_post,omitempty"`          // as berth run --drain-post
	DrainSlots   string `toml:"drain_slots,omitempty" json:"drain_slots,omitempty" yaml:"drain_slots,omitempty"`       // as berth run --drain-slots
	DrainComfyUI string `toml:"drain_comfyui,omitempty" json:"drain_comfyui,omitempty" yaml:"drain_comfyui,omitempty"` // as berth run --drain-comfyui
	// KeepComfyUI is berth run --keep-comfyui-up: unload and stay up.
	KeepComfyUI bool `toml:"keep_comfyui_up,omitempty" json:"keep_comfyui_up,omitempty" yaml:"keep_comfyui_up,omitempty"`
	// DrainMax is berth run --drain-max: a ceiling on how long a drain may
	// wait at a boundary (a processing slot, a running ComfyUI job) before
	// it stops the engine anyway. 0 = unbounded, the default.
	DrainMax Duration `toml:"drain_max,omitzero" json:"drain_max,omitempty" yaml:"drain_max,omitempty"`
	// MinTurn is berth run --min-turn: a minimum turn. For this long after
	// the grant the lease keeps the card; no request drains it in that
	// time, whatever its priority, only a hold takes the card out. When
	// the turn ends the normal rules apply. 0 = off, the default.
	MinTurn Duration `toml:"min_turn,omitzero" json:"min_turn,omitempty" yaml:"min_turn,omitempty"`
	// Container is the engine's container, as berth run --container: the
	// name of `docker run --name` (or of an existing container for
	// `docker start -a`). The drain stops it through the runtime CLI
	// instead of signalling the wrapper; Linux-only in this release.
	Container string `toml:"container,omitempty" json:"container,omitempty" yaml:"container,omitempty"`
	// Park picks the RAM tier on a drain: off (the default) runs the
	// plain drain, hook asks the engine to move its weights to system
	// RAM instead, so the next use resumes in seconds. Engines with a
	// built-in adapter (comfyui, vllm, sglang) need no URLs; a kind
	// outside the table with no URLs uses the Berth park protocol if
	// the engine answers GET /berth/state, and drains as today if it
	// does not. One URL without the other is an error.
	Park string `toml:"park,omitempty" json:"park,omitempty" yaml:"park,omitempty"`
	// ParkURL and ResumeURL are the hook's calls; each, when set, beats
	// the built-in adapter for its side.
	ParkURL   string `toml:"park_url,omitempty" json:"park_url,omitempty" yaml:"park_url,omitempty"`
	ResumeURL string `toml:"resume_url,omitempty" json:"resume_url,omitempty" yaml:"resume_url,omitempty"`
	// GUI is the engine's web UI as a browser reaches it. With start set, the
	// gateway's wake page (/gui/<name>) starts the engine on a visit and
	// follows it there once it answers.
	GUI string `toml:"gui,omitempty" json:"gui,omitempty" yaml:"gui,omitempty"`
}

// Types are the engine readers that exist.
var Types = []string{"llama-swap", "llamacpp", "ninfer", "strata", "ollama", "vllm", "sglang", "koboldcpp",
	"tabbyapi", "localai", "lmstudio", "unsloth", "freetoken", "openai", "comfyui", "kokoro", "custom"}

// Kinds are what an engine can produce (custom engines name theirs).
var Kinds = []string{"llm", "image", "tts", "stt", "other"}

// MapFields are the fields a custom engine may map to its metric names.
var MapFields = []string{"prompt_tokens", "output_tokens", "prompt_seconds", "output_seconds", "processing", "waiting"}

type Config struct {
	Metrics struct {
		// Listen is where /metrics is served for Prometheus. Loopback only;
		// empty = off.
		Listen string `toml:"listen" json:"listen,omitempty" yaml:"listen,omitempty"`
	} `toml:"metrics,omitempty" json:"metrics" yaml:"metrics"`
	Engines struct {
		Every Duration `toml:"every" json:"every" yaml:"every"` // how often engines are read; default 5 s
		// Discover probes the well-known engine ports on this machine (and
		// on Hosts) every minute and reads whatever answers; default on.
		// Ports adds more ports to probe with every reader.
		Discover *bool    `toml:"discover" json:"discover" yaml:"discover"`
		Hosts    []string `toml:"discover_hosts,omitempty" json:"discover_hosts,omitempty" yaml:"discover_hosts,omitempty"`
		Ports    []int    `toml:"discover_ports,omitempty" json:"discover_ports,omitempty" yaml:"discover_ports,omitempty"`
	} `toml:"engines" json:"engines" yaml:"engines"`
	Engine   []Engine       `toml:"engine,omitempty" json:"engine,omitempty" yaml:"engine,omitempty"`
	Alerts   AlertsConfig   `toml:"alerts" json:"alerts" yaml:"alerts"`
	Priority PriorityConfig `toml:"priority,omitempty" json:"priority" yaml:"priority"`
	Claims   ClaimsConfig   `toml:"claims,omitempty" json:"claims" yaml:"claims"`
	Claim    []ClaimRule    `toml:"claim,omitempty" json:"claim,omitempty" yaml:"claim,omitempty"`
	Park     ParkConfig     `toml:"park,omitempty" json:"park,omitempty" yaml:"park,omitempty"`
	Models   ModelsConfig   `toml:"models,omitempty" json:"models,omitempty" yaml:"models,omitempty"`
	Library  LibraryConfig  `toml:"library,omitempty" json:"library,omitempty" yaml:"library,omitempty"`
	Update   UpdateConfig   `toml:"update,omitempty" json:"update,omitempty" yaml:"update,omitempty"`
}

// UpdateConfig switches the check for a newer release. On, the harbor asks
// the GitHub API once every 24 h, one GET to releases/latest, and shows a
// new release in its header. Off, Berth never contacts GitHub on its own;
// typing `berth update` is the operator asking, and still reaches it.
type UpdateConfig struct {
	Check *bool `toml:"check" json:"check" yaml:"check"` // default on
}

// UpdateCheck is the configured switch, on when the config names none.
func (c *Config) UpdateCheck() bool {
	if c.Update.Check != nil {
		return *c.Update.Check
	}
	return true
}

// ClaimsConfig switches the built-in claim pack (Steam, Lutris and gamescope
// games hold the card; Blender, Resolve, OBS and Kdenlive are tracked).
type ClaimsConfig struct {
	Builtin *bool    `toml:"builtin" json:"builtin" yaml:"builtin"` // default on
	Every   Duration `toml:"every" json:"every" yaml:"every"`       // process scan period; default 2 s
}

// ClaimRule is one [[claim]]: work Berth does not run that holds the card
// (action "hold", the default) or has its card memory named ("track").
type ClaimRule struct {
	Name     string   `toml:"name" json:"name" yaml:"name"`
	Exe      []string `toml:"exe,omitempty" json:"exe,omitempty" yaml:"exe,omitempty"`
	Cmdline  string   `toml:"cmdline,omitempty" json:"cmdline,omitempty" yaml:"cmdline,omitempty"`
	Not      string   `toml:"not_cmdline,omitempty" json:"not_cmdline,omitempty" yaml:"not_cmdline,omitempty"`
	Parent   []string `toml:"parent,omitempty" json:"parent,omitempty" yaml:"parent,omitempty"`
	Action   string   `toml:"action,omitempty" json:"action,omitempty" yaml:"action,omitempty"`
	NeedsMiB uint64   `toml:"needs_mib,omitzero" json:"needs_mib,omitempty" yaml:"needs_mib,omitempty"`
	Linger   Duration `toml:"linger,omitempty" json:"linger,omitempty" yaml:"linger,omitempty"`
}

// ClaimRules is the rule list berthd runs: the configured rules first (a
// configured rule wins a process over a built-in), then the built-in pack
// unless it is off, minus any built-in a configured rule renames.
func (c *Config) ClaimRules() ([]claims.Rule, error) {
	var out []claims.Rule
	names := map[string]bool{}
	for _, r := range c.Claim {
		rule := claims.Rule{Name: r.Name, Exe: r.Exe, Cmdline: r.Cmdline, Not: r.Not, Parent: r.Parent,
			Action: r.Action, NeedsMiB: r.NeedsMiB, Linger: r.Linger.Duration}
		if err := rule.Compile(); err != nil {
			return nil, err
		}
		if names[rule.Name] {
			return nil, fmt.Errorf("claim %q is defined twice", rule.Name)
		}
		names[rule.Name] = true
		out = append(out, rule)
	}
	if c.Claims.Builtin == nil || *c.Claims.Builtin {
		for _, rule := range claims.Builtin() {
			if names[rule.Name] {
				continue
			}
			if err := rule.Compile(); err != nil {
				return nil, err
			}
			out = append(out, rule)
		}
	}
	return out, nil
}

// ModelsConfig is where the model library is: the extra directories
// `berth models` scans when the command line names none. The tools' own
// directories (the Hugging Face cache, LM Studio, Ollama's store, ~/models) are
// scanned anyway, so this is only for a library kept somewhere else.
type ModelsConfig struct {
	Dirs []string `toml:"dirs,omitempty" json:"dirs,omitempty" yaml:"dirs,omitempty"`
}

// DefaultFreeFloor is the share of a root's total space a pull must leave
// free. A drive never fills: a pull that would go under it is refused.
const DefaultFreeFloor = 0.10

// LibraryConfig is the model library's placement rules: how much space a
// root must keep free, and what a root is - which shelf it holds and which
// other OS reads it.
type LibraryConfig struct {
	// FreeFloor is the fraction of a root's total space that stays free
	// after any pull; nil keeps the default, 0 turns the guard off.
	FreeFloor *float64 `toml:"free_floor,omitempty" json:"free_floor,omitempty" yaml:"free_floor,omitempty"`
	// Root declares facts about a specific root: its shelf and the other OS
	// it is shared with. Paths that are not declared take the defaults:
	// the shelf is whatever a file states, and the root is local only.
	Root []LibraryRoot `toml:"root,omitempty" json:"root,omitempty" yaml:"root,omitempty"`
}

// LibraryRoot is one root's declared facts.
type LibraryRoot struct {
	Path string `toml:"path" json:"path" yaml:"path"`
	// Shelf declares what the whole root holds; it beats what a file states.
	Shelf string `toml:"shelf,omitempty" json:"shelf,omitempty" yaml:"shelf,omitempty"`
	// Shared names the other OS that reads this root, like "windows". A
	// shared root never gets symlinks, and a move off it needs consent.
	Shared string `toml:"shared,omitempty" json:"shared,omitempty" yaml:"shared,omitempty"`
}

// LibraryFloor is the configured floor, the default when the config names
// none.
func (c *Config) LibraryFloor() float64 {
	if c.Library.FreeFloor != nil {
		return *c.Library.FreeFloor
	}
	return DefaultFreeFloor
}

// ParkConfig is the node's park budget: the RAM parked engines may hold
// and how long they may hold it. Parked RAM is a reading Berth can take
// back at any tick, never a reservation.
type ParkConfig struct {
	// FloorGiB is the RAM, on top of the gateway's headroom floor, that
	// a park must leave free; nil keeps the default, 0 turns the park
	// margin off.
	FloorGiB *float64 `toml:"floor_gib,omitempty" json:"floor_gib,omitempty" yaml:"floor_gib,omitempty"`
	// MaxGiB caps the RAM all parked engines may hold together; 0 = no
	// cap.
	MaxGiB float64 `toml:"max_gib,omitempty" json:"max_gib,omitempty" yaml:"max_gib,omitempty"`
	// TTL is how long a parked engine stays parked before Berth stops it.
	TTL Duration `toml:"ttl" json:"ttl" yaml:"ttl"`
	// ResidualMaxGiB is how much card memory a parked engine may keep
	// (its CUDA context, and any weights that did not move) and still
	// count as parked; a whole-card grant is allowed beside it. More than
	// this after the park call, past the release grace, stops the engine
	// as any drain. 0 = the default, 2 GiB.
	ResidualMaxGiB float64 `toml:"residual_max_gib,omitempty" json:"residual_max_gib,omitempty" yaml:"residual_max_gib,omitempty"`
	// Pressure is the /proc/pressure/memory "some avg10" figure that
	// evicts a parked engine even when the RAM floor holds. 0 = the
	// default, 10.0.
	Pressure float64 `toml:"pressure,omitempty" json:"pressure,omitempty" yaml:"pressure,omitempty"`
}

// DefaultParkFloorGiB is the RAM a park leaves free when the config
// names no floor, in GiB.
const DefaultParkFloorGiB = 4.0

// DefaultParkTTL is how long a parked engine stays parked when the
// config names no ttl.
const DefaultParkTTL = 30 * time.Minute

// gib is a GiB in bytes.
const gib = uint64(1) << 30

// ParkFloorGiB is the configured floor, or the default when the config
// names none.
func (c *Config) ParkFloorGiB() float64 {
	if c.Park.FloorGiB != nil {
		return *c.Park.FloorGiB
	}
	return DefaultParkFloorGiB
}

// ParkTTL is the configured ttl, or the default when the config names
// none.
func (c *Config) ParkTTL() time.Duration {
	if c.Park.TTL.Duration > 0 {
		return c.Park.TTL.Duration
	}
	return DefaultParkTTL
}

// ParkResidualMax is the configured residual a parked engine may keep on
// the card, in bytes, or the default (2 GiB).
func (c *Config) ParkResidualMax() uint64 {
	if c.Park.ResidualMaxGiB > 0 {
		return uint64(c.Park.ResidualMaxGiB * float64(gib))
	}
	return 2 * gib
}

// ParkPressure is the configured memory-pressure threshold that evicts a
// parked engine, or the default (10.0 "some avg10").
func (c *Config) ParkPressure() float64 {
	if c.Park.Pressure > 0 {
		return c.Park.Pressure
	}
	return 10.0
}

// PriorityConfig decides juggles: two engines at the same priority asking for
// the card within JuggleWindow of each other while the holder is busy.
type PriorityConfig struct {
	// Order ranks engines, first is best: a lease's --group, else its
	// engine name. Empty = the engine with the most card-hours over the
	// last 7 days ranks first.
	Order []string `toml:"order,omitempty" json:"order,omitempty" yaml:"order,omitempty"`
	// JuggleWindow: default 15s; "0s" keeps the default, a negative value
	// turns juggle handling off (last switch always wins).
	JuggleWindow Duration `toml:"juggle_window" json:"juggle_window" yaml:"juggle_window"`
}

// AlertsConfig is the threshold table the node evaluates on its sample cycle.
// A key with no entry is not watched. probe and stuck are built in; they
// cannot be configured off.
type AlertsConfig struct {
	ClearPolls *int            `toml:"clear_polls,omitempty" json:"clear_polls,omitempty" yaml:"clear_polls,omitempty"` // consecutive clean polls before a level drops; nil = 3
	Queue      proto.Threshold `toml:"queue,omitempty" json:"queue" yaml:"queue"`                                       // waiting leases
	TTFT       proto.Threshold `toml:"ttft,omitempty" json:"ttft" yaml:"ttft"`                                          // prefill time, ms
	TPOT       proto.Threshold `toml:"tpot,omitempty" json:"tpot" yaml:"tpot"`                                          // ms per output token
	P95        proto.Threshold `toml:"p95,omitempty" json:"p95" yaml:"p95"`                                             // request duration, ms
	Cache      proto.Threshold `toml:"cache,omitempty" json:"cache" yaml:"cache"`                                       // cache share, %; low is bad
	Reingest   proto.Threshold `toml:"reingest,omitempty" json:"reingest" yaml:"reingest"`                              // re-ingest share, %
	VRAM       proto.Threshold `toml:"vram,omitempty" json:"vram" yaml:"vram"`                                          // fraction of the card
	Temp       proto.Threshold `toml:"temp,omitempty" json:"temp" yaml:"temp"`                                          // °C
	Power      proto.Threshold `toml:"power,omitempty" json:"power" yaml:"power"`                                       // fraction of the power limit
	KV         proto.Threshold `toml:"kv,omitempty" json:"kv" yaml:"kv"`                                                // deepest slot fill, 0..1
}

// Duration is a TOML string like "5s". It keeps that spelling in JSON and
// YAML too, so the three encodings of the config read the same.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("a duration is a string like \"5s\"")
	}
	return d.UnmarshalText([]byte(s))
}

func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("a duration is a string like \"5s\"")
	}
	return d.UnmarshalText([]byte(node.Value))
}

// DefaultPath is $BERTH_CONFIG, else $XDG_CONFIG_HOME/berth/berth.toml, else
// the system's per-user config directory (os.UserConfigDir: ~/.config on
// Unix, %AppData% on Windows).
func DefaultPath() string {
	if p := os.Getenv("BERTH_CONFIG"); p != "" {
		return p
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "berth", "berth.toml")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "berth.toml"
	}
	return filepath.Join(dir, "berth", "berth.toml")
}

// Load reads and validates path, with the defaults of the file's schema
// materialised. A missing file is an empty config, not an error; an unreadable
// or invalid one is.
func Load(path string) (Config, error) {
	c, _, err := Check(path)
	return c, err
}

// Validate reports every problem at once, so one run of `berth config check`
// shows everything to fix.
func (c *Config) Validate() error {
	var errs []error
	if c.Engines.Every.Duration < 0 {
		errs = append(errs, errors.New("engines.every must be positive"))
	}
	if c.Metrics.Listen != "" {
		if err := Loopback(c.Metrics.Listen); err != nil {
			errs = append(errs, fmt.Errorf("metrics.listen: %w", err))
		}
	}
	if _, err := c.ClaimRules(); err != nil {
		errs = append(errs, err)
	}
	if cp := c.Alerts.ClearPolls; cp != nil && *cp < 1 {
		errs = append(errs, errors.New("alerts.clear_polls must be >= 1"))
	}
	// frac: a fraction of a limit (vram, power, kv) - any set level in (0, 1].
	// low: lower is worse (cache) - when both levels are set, crit < warn.
	alertKeys := []struct {
		name string
		frac bool
		low  bool
		thr  proto.Threshold
	}{
		{"queue", false, false, c.Alerts.Queue},
		{"ttft", false, false, c.Alerts.TTFT},
		{"tpot", false, false, c.Alerts.TPOT},
		{"p95", false, false, c.Alerts.P95},
		{"cache", false, true, c.Alerts.Cache},
		{"reingest", false, false, c.Alerts.Reingest},
		{"vram", true, false, c.Alerts.VRAM},
		{"temp", false, false, c.Alerts.Temp},
		{"power", true, false, c.Alerts.Power},
		{"kv", true, false, c.Alerts.KV},
	}
	for _, k := range alertKeys {
		if k.thr.Warn == 0 && k.thr.Crit == 0 {
			continue // the key is not watched
		}
		for _, l := range []struct {
			name string
			v    float64
		}{
			{"warn", k.thr.Warn},
			{"crit", k.thr.Crit},
		} {
			if l.v == 0 {
				continue // level not set
			}
			where := fmt.Sprintf("alerts.%s: %s %g", k.name, l.name, l.v)
			if k.frac && (l.v <= 0 || l.v > 1) {
				errs = append(errs, errors.New(where+" must be in (0, 1]"))
			} else if l.v < 0 {
				errs = append(errs, errors.New(where+" must be > 0"))
			}
		}
		if k.thr.Warn != 0 && k.thr.Crit != 0 {
			if k.low && k.thr.Crit >= k.thr.Warn {
				errs = append(errs, fmt.Errorf("alerts.%s: crit %g must be < warn %g", k.name, k.thr.Crit, k.thr.Warn))
			} else if !k.low && !k.frac && k.thr.Crit < k.thr.Warn {
				errs = append(errs, fmt.Errorf("alerts.%s: crit %g must be >= warn %g", k.name, k.thr.Crit, k.thr.Warn))
			}
		}
	}
	ordered := map[string]bool{}
	for i, o := range c.Priority.Order {
		switch {
		case strings.TrimSpace(o) == "":
			errs = append(errs, fmt.Errorf("priority.order[%d] is empty", i))
		case ordered[o]:
			errs = append(errs, fmt.Errorf("priority.order lists %q twice", o))
		}
		ordered[o] = true
	}
	seen := map[string]bool{}
	served := map[string]string{} // model -> the engine that serves it
	for i, e := range c.Engine {
		where := fmt.Sprintf("engine[%d] %q", i, e.Name)
		if e.Name == "" {
			errs = append(errs, fmt.Errorf("engine[%d]: name is required", i))
		} else if seen[e.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[e.Name] = true
		if !slices.Contains(Types, e.Type) {
			errs = append(errs, fmt.Errorf("%s: type %q is not one of %v", where, e.Type, Types))
		}
		if e.Kind != "" && !slices.Contains(Kinds, e.Kind) {
			errs = append(errs, fmt.Errorf("%s: kind %q is not one of %v", where, e.Kind, Kinds))
		}
		if u, err := url.Parse(e.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%s: url %q must be http(s)://host:port", where, e.URL))
		}
		if e.Type == "custom" && len(e.Map) == 0 && e.Health == "" && e.Models == "" && e.Metrics == "" && e.Slots == "" {
			errs = append(errs, fmt.Errorf("%s: a custom engine needs at least one of health, models, metrics, slots or [engine.map]", where))
		}
		for _, m := range e.Serves {
			if other, ok := served[m]; ok {
				errs = append(errs, fmt.Errorf("%s: serves %q, which %q already serves; the gateway would never pick this one", where, m, other))
			} else {
				served[m] = e.Name
			}
		}
		if len(e.Start) > 0 && len(e.Serves) == 0 && e.Type != "comfyui" {
			errs = append(errs, fmt.Errorf("%s: start needs serves, the models the gateway may start it for", where))
		}
		if e.Type == "comfyui" && len(e.Serves) > 0 {
			errs = append(errs, fmt.Errorf("%s: a comfyui engine is reached at the gateway's /prompt, not through serves (ComfyUI is not OpenAI-compatible)", where))
		}
		if e.DrainSlots != "" {
			if u, err := url.Parse(e.DrainSlots); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: drain_slots %q must be the engine's http(s)://host:port", where, e.DrainSlots))
			}
		}
		if e.DrainMax.Duration < 0 {
			errs = append(errs, fmt.Errorf("%s: drain_max %s must be >= 0", where, e.DrainMax))
		}
		if e.MinTurn.Duration < 0 {
			errs = append(errs, fmt.Errorf("%s: min_turn %s must be >= 0", where, e.MinTurn))
		}
		if e.GUI != "" {
			if u, err := url.Parse(e.GUI); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: gui %q must be an http(s) URL", where, e.GUI))
			} else if len(e.Start) == 0 {
				errs = append(errs, fmt.Errorf("%s: gui needs start, the command the wake page starts the engine with", where))
			}
		}
		if e.KeepComfyUI && e.DrainComfyUI == "" {
			errs = append(errs, fmt.Errorf("%s: keep_comfyui_up needs drain_comfyui, the ComfyUI base URL to unload on drain", where))
		}
		switch e.Park {
		case "", "off", "hook":
		default:
			errs = append(errs, fmt.Errorf("%s: park %q is not off or hook", where, e.Park))
		}
		for _, u := range []struct {
			key string
			url string
		}{{"park_url", e.ParkURL}, {"resume_url", e.ResumeURL}} {
			if u.url == "" {
				continue
			}
			if p, err := url.Parse(u.url); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
				errs = append(errs, fmt.Errorf("%s: %s %q must be an absolute http(s) URL", where, u.key, u.url))
			}
		}
		if e.Park == "hook" && !slices.Contains(park.BuiltInKinds(), e.Type) && (e.ParkURL == "") != (e.ResumeURL == "") {
			errs = append(errs, fmt.Errorf("%s: park = \"hook\" for a %s engine needs both park_url and resume_url, or neither: with neither, Berth tries the Berth park protocol", where, e.Type))
		}
		switch e.Priority {
		case "", "batch", "interactive", "foreground":
		default:
			errs = append(errs, fmt.Errorf("%s: priority %q is not batch, interactive or foreground", where, e.Priority))
		}
		for k := range e.Map {
			if !slices.Contains(MapFields, k) {
				errs = append(errs, fmt.Errorf("%s: map field %q is not one of %v", where, k, MapFields))
			}
		}
	}
	for _, p := range c.Engines.Ports {
		if p <= 0 || p > 65535 {
			errs = append(errs, fmt.Errorf("engines.discover_ports: %d is not a port", p))
		}
	}
	if f := c.Library.FreeFloor; f != nil {
		if *f < 0 || *f > 1 {
			errs = append(errs, fmt.Errorf("library.free_floor %g must be a fraction in [0, 1]", *f))
		}
	}
	rootSeen := map[string]bool{}
	for i, r := range c.Library.Root {
		where := fmt.Sprintf("library.root[%d]", i)
		if strings.TrimSpace(r.Path) == "" {
			errs = append(errs, fmt.Errorf("%s: path is required", where))
		} else if rootSeen[r.Path] {
			errs = append(errs, fmt.Errorf("%s: %q is declared twice", where, r.Path))
		} else {
			rootSeen[r.Path] = true
		}
		if r.Shelf != "" && !shelfValid(r.Shelf) {
			errs = append(errs, fmt.Errorf("%s: shelf %q is not chat, image or audio", where, r.Shelf))
		}
	}
	if f := c.Park.FloorGiB; f != nil && *f < 0 {
		errs = append(errs, fmt.Errorf("park.floor_gib %g must be >= 0", *f))
	}
	if c.Park.MaxGiB < 0 {
		errs = append(errs, fmt.Errorf("park.max_gib %g must be >= 0", c.Park.MaxGiB))
	}
	if c.Park.TTL.Duration < 0 {
		errs = append(errs, errors.New("park.ttl must not be negative"))
	}
	return errors.Join(errs...)
}

// Loopback accepts host:port only when host is a loopback address or
// localhost. Berth never serves on a LAN address by accident.
func Loopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q is not a loopback address (use 127.0.0.1 or [::1])", host)
	}
	return nil
}

func shelfValid(s string) bool {
	switch s {
	case "chat", "image", "audio":
		return true
	}
	return false
}
