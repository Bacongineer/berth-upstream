// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package suite manages the engine installs Berth runs as a suite: each
// install is a set of git components pinned to a commit, with one previous
// commit kept as the rollback snapshot. A component is "a git checkout
// pinned to a commit"; the core is just the component whose path is the
// install root. Everything the suite does to a component (snapshot, update,
// rollback, dirty check) is the same git operations, which is what makes node
// packs free: no per-pack code.
package suite

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/fsx"
)

// KindComfyUI is the only install kind the suite manages today; the state
// shape is kind-neutral, so a later kind adds a recipe, not a migration.
const KindComfyUI = "comfyui"

// State is the contents of the suite state file: the installs, their pinned
// components, and each component's rollback snapshot.
type State struct {
	Installs []Install `toml:"install"`
}

// Install is one engine Berth manages: a checkout on a port, plus the
// components inside it (the core and each node pack).
type Install struct {
	Name       string      `toml:"name"`
	Kind       string      `toml:"kind"`
	Path       string      `toml:"path"` // the checkout; adopted in place or the clone target
	Port       int         `toml:"port"`
	Priority   string      `toml:"priority"` // batch, interactive or foreground
	KeepUp     bool        `toml:"keep_up,omitempty"`
	Venv       string      `toml:"venv,omitempty"` // relative to Path; empty = python3 on PATH
	Components []Component `toml:"component"`
}

// Component is a git checkout pinned to a commit. An empty Path is the
// install root: the core component.
type Component struct {
	Name     string `toml:"name"`
	Repo     string `toml:"repo"`    // empty when the checkout has no origin remote: rollback only
	Channel  string `toml:"channel"` // the remote ref update follows; empty = detached, --channel required
	Path     string `toml:"path,omitempty"`
	Commit   string `toml:"commit"`
	Previous string `toml:"previous,omitempty"` // the pin before the last change; rollback target
	Dirty    bool   `toml:"dirty,omitempty"`    // the working tree has local changes
}

// ComponentDir resolves the component's checkout directory.
func (i Install) ComponentDir(c Component) string {
	if c.Path == "" {
		return i.Path
	}
	return filepath.Join(i.Path, c.Path)
}

// Path is the suite state file for the default berth.toml: $BERTH_ENGINES,
// else engines.toml beside it.
func Path() string { return SuitePath(config.DefaultPath()) }

// SuitePath is the suite state file for the berth.toml at path: $BERTH_ENGINES,
// else engines.toml beside it.
func SuitePath(path string) string {
	if p := os.Getenv("BERTH_ENGINES"); p != "" {
		return p
	}
	return filepath.Join(filepath.Dir(path), "engines.toml")
}

// Load reads the state file at path. A missing file is an empty state, not an
// error, mirroring config.Check; a file that does not parse is.
func Load(path string) (State, error) {
	var st State
	_, err := toml.DecodeFile(path, &st)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

// Save writes the state atomically (see fsx.WriteFile), so a crash never
// leaves a half-written file.
func Save(path string, st State) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(st); err != nil {
		return err
	}
	return fsx.WriteFile(path, buf.Bytes(), 0o644)
}

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	shaPattern  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// Validate reports every problem at once, so one run shows everything to fix.
func (st State) Validate() error {
	var errs []error
	seenName := map[string]bool{}
	seenPort := map[int]string{}
	for i, in := range st.Installs {
		where := fmt.Sprintf("install[%d] %q", i, in.Name)
		if in.Name == "" {
			errs = append(errs, fmt.Errorf("install[%d]: name is required", i))
		} else if !namePattern.MatchString(in.Name) {
			errs = append(errs, fmt.Errorf("%s: name must be lowercase letters, digits and dashes, starting with a letter or digit", where))
		} else if seenName[in.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seenName[in.Name] = true
		if in.Kind != KindComfyUI {
			errs = append(errs, fmt.Errorf("%s: kind %q is not %s", where, in.Kind, KindComfyUI))
		}
		if in.Path == "" {
			errs = append(errs, fmt.Errorf("%s: path is required", where))
		}
		if in.Port < 1024 || in.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s: port %d is not in 1024..65535", where, in.Port))
		} else if other, ok := seenPort[in.Port]; ok {
			errs = append(errs, fmt.Errorf("%s: port %d is already used by %q", where, in.Port, other))
		}
		seenPort[in.Port] = in.Name
		switch in.Priority {
		case "batch", "interactive", "foreground":
		default:
			errs = append(errs, fmt.Errorf("%s: priority %q is not batch, interactive or foreground", where, in.Priority))
		}
		core := 0
		seenComp := map[string]bool{}
		for j, cp := range in.Components {
			cw := fmt.Sprintf("%s component[%d] %q", where, j, cp.Name)
			if cp.Name == "" {
				errs = append(errs, fmt.Errorf("%s component[%d]: name is required", where, j))
			} else if seenComp[cp.Name] {
				errs = append(errs, fmt.Errorf("%s: duplicate component name", cw))
			}
			seenComp[cp.Name] = true
			if cp.Name == KindComfyUI {
				core++
				if cp.Path != "" {
					errs = append(errs, fmt.Errorf("%s: the core lives at the install root; its path must be empty", where))
				}
			}
			if cp.Commit != "" && !shaPattern.MatchString(cp.Commit) {
				errs = append(errs, fmt.Errorf("%s: commit %q is not a hex sha of 7..40", cw, cp.Commit))
			}
			if cp.Previous != "" && !shaPattern.MatchString(cp.Previous) {
				errs = append(errs, fmt.Errorf("%s: previous %q is not a hex sha of 7..40", cw, cp.Previous))
			}
			if cp.Repo != "" {
				if u, err := url.Parse(cp.Repo); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
					errs = append(errs, fmt.Errorf("%s: repo %q must be an http(s) URL", cw, cp.Repo))
				}
			}
		}
		if core != 1 {
			errs = append(errs, fmt.Errorf("%s: needs exactly one component named %q at the install root", where, KindComfyUI))
		}
	}
	return errors.Join(errs...)
}
