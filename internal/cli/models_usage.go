// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/models"
	"github.com/T-Crypt/berth/internal/store"
)

// The listing answers "which of these files have I actually run, and when".
// Only the harbor's request log knows that, and Berth is only ever a reader of
// it: the database is opened mode=ro and a harbor that was never run is an
// empty answer, not a failed command.

// parseWindow is a --usage window: 30d, 24h, 90m, 2w. Go's durations have no
// day, and nobody types 720h0m0s.
func parseWindow(s string) (time.Duration, error) {
	v := strings.TrimSpace(strings.ToLower(s))
	if v == "" {
		return 0, errors.New("empty window")
	}
	switch v[len(v)-1] {
	case 'd', 'w':
		n := mustNum(v[:len(v)-1])
		if n == 0 {
			return 0, errors.New("want a window like 30d or 2w")
		}
		if v[len(v)-1] == 'w' {
			return time.Duration(n) * 7 * 24 * time.Hour, nil
		}
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h', 'm', 's':
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, err
		}
		if d <= 0 {
			return 0, errors.New("want a window into the past")
		}
		return d, nil
	}
	return 0, errors.New("want a window like 30d, 24h or 90m")
}

// mustNum is a window's number: what is not a number is zero, and a zero window
// is refused by the caller's check on the result.
func mustNum(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// humanWindow is a window as its column heading reads it.
func humanWindow(d time.Duration) string {
	switch {
	case d%(7*24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(7*24*time.Hour)), 10) + "w"
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
}

// swapIDs is every llama-swap model id and the file its cmd loads, from the
// config of whichever configured engine is llama-swap. Nothing else knows that
// a request for "Qwen3-8B" was served by /models/qwen3-8b-q4_k_m.gguf, and an
// engine configured without a start command (the usual, when llama-swap is
// started by hand) simply has no ids to offer.
func swapIDs(cfg config.Config) map[string]string {
	for _, e := range cfg.Engine {
		if e.Type != "llama-swap" {
			continue
		}
		if p := swapConfigPath(e.Start); p == "" {
			continue
		} else if ids := models.SwapIDs(p, nil); ids != nil {
			return ids
		}
	}
	// No configured start line: llama-swap was started by a unit or by hand,
	// and its own command line still names the config.
	if p, env := runningSwapConfig(); p != "" {
		return models.SwapIDs(p, env)
	}
	return nil
}

// runningSwapConfig is the --config of a running llama-swap and its
// environment, read from /proc. "" off Linux or when none runs; a nil env when
// the process belongs to another user.
func runningSwapConfig() (string, map[string]string) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return "", nil
	}
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if filepath.Base(args[0]) != "llama-swap" {
			continue
		}
		if p := swapConfigPath(args); p != "" {
			return p, procEnviron(filepath.Join("/proc", e.Name(), "environ"))
		}
	}
	return "", nil
}

func procEnviron(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(raw), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// swapConfigPath is the --config of a llama-swap command line.
func swapConfigPath(start []string) string {
	for i, a := range start {
		for _, flag := range []string{"--config", "-config", "--config.path", "-c"} {
			if a == flag && i+1 < len(start) {
				return start[i+1]
			}
			if eq := flag + "="; strings.HasPrefix(a, eq) {
				return strings.Trim(strings.TrimPrefix(a, eq), `"'`)
			}
		}
	}
	return ""
}

// attachUsage reads the harbor read-only and puts the result on the models
// (Model.Usage), the window's counts in the window and the USED column over all
// the log retains. Anything the log names and no file answers to goes to
// doc.Unmatched, which only the machine output shows.
func attachUsage(c *Context, ms []models.Model, ids map[string]string, since time.Time, doc *modelsDoc) {
	path := defaultHarborDB()
	st, err := store.OpenReadOnly(path)
	if err != nil {
		if !errors.Is(err, store.ErrNoStore) && !c.Machine() {
			c.Warnf("%s: no usage data (%v)", path, err)
		}
		return
	}
	defer st.Close()

	// One query, however many windows the fold wants: the rows are read once and
	// kept, and a library of thirty models asks the database once.
	var cached []models.ServedRequest
	read := func(from time.Time) ([]models.ServedRequest, error) {
		if cached == nil {
			rows, err := st.RequestsSince(time.Time{})
			if err != nil {
				return nil, err
			}
			cached = make([]models.ServedRequest, 0, len(rows))
			for _, r := range rows {
				cached = append(cached, models.ServedRequest{
					T: r.T, Engine: r.Engine, Model: r.Model, Prompt: r.Prompt, Output: r.Output,
				})
			}
			// A database with no requests at all still has to read as empty
			// rather than as unread, so a nil slice is replaced by an empty one.
			if cached == nil {
				cached = []models.ServedRequest{}
			}
		}
		if from.IsZero() {
			return cached, nil
		}
		var out []models.ServedRequest
		for _, r := range cached {
			if !r.T.Before(from) {
				out = append(out, r)
			}
		}
		return out, nil
	}

	// The whole log decides the USED column; the window, if any, decides REQ and
	// TOKENS. Two folds, because a model used 40 days ago is both "40d ago" and
	// idle this week.
	all, err := models.Usage(read, time.Time{})
	if err != nil {
		if !c.Machine() {
			c.Warnf("%s: no usage data (%v)", path, err)
		}
		return
	}
	orphan := models.MatchUsage(ms, all, ids)
	if !since.IsZero() {
		window, err := models.Usage(read, since)
		if err != nil {
			if !c.Machine() {
				c.Warnf("%s: no windowed usage data (%v)", path, err)
			}
			return
		}
		// A copy, because MatchUsage credits what it is given and the all-time
		// LastUsed must survive the second pass.
		win := make([]models.Model, len(ms))
		copy(win, ms)
		for i := range win {
			win[i].Usage = nil
		}
		models.MatchUsage(win, window, ids)
		for i := range ms {
			if win[i].Usage == nil {
				if ms[i].Usage != nil {
					ms[i].Usage.Requests = 0 // seen in the log, idle in the window
					ms[i].Usage.PromptTokens = 0
					ms[i].Usage.OutputTokens = 0
				}
				continue
			}
			last := ms[i].Usage
			ms[i].Usage = win[i].Usage
			if last != nil {
				ms[i].Usage.LastUsed = last.LastUsed
			}
		}
	}
	for _, key := range sortedServed(orphan) {
		u := orphan[key]
		name := key // the first spelling the requests actually used, when there was one
		if len(u.Names) > 0 {
			name = u.Names[0]
		}
		doc.Unmatched = append(doc.Unmatched, servedDoc{
			Model: name, Engines: u.Engines, Requests: u.Requests,
			PromptTokens: u.PromptTokens, OutputTokens: u.OutputTokens, LastUsed: u.LastUsed,
		})
	}
}

// sortedServed is the orphans in a stable order: by name, which is what the
// listing shows, the same way every other list in Berth is ordered.
func sortedServed(m map[string]models.ModelUsage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	name := func(k string) string {
		if n := m[k].Names; len(n) > 0 {
			return n[0]
		}
		return k
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := name(out[i]), name(out[j])
		if a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

// modelRoots is the directory list a name resolves against: the command's --dir
// flags, then berth.toml's [models] dirs, then nothing - which the scan reads as
// this machine's tool directories. A name has to mean the same library in
// `models`, `models show` and `models export`.
func modelRoots(c *Context, dirs []string) []string {
	if len(dirs) > 0 {
		return dirs
	}
	cfg, _, _, err := loadConfig(c, config.DefaultPath())
	if err != nil {
		return nil
	}
	return cfg.Models.Dirs
}
