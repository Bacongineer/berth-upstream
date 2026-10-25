// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package models

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// The harbor's request log is the only record of which model actually served
// what: a file on disk that no engine has opened in a month is a different
// thing from a file on disk that answered four thousand prompts last week.
// Nothing here writes - the caller opens the database read-only.

// ServedRequest is one request row, the part of it a listing needs.
type ServedRequest struct {
	T      time.Time
	Engine string
	Model  string
	Prompt float64 // prompt tokens, 0 when the engine did not say
	Output float64
}

// RowsFunc reads the served requests at or after since. A nil RowsFunc is no
// harbor database at all, which is not an error: it means no usage.
type RowsFunc func(since time.Time) ([]ServedRequest, error)

// ModelUsage is what the log says one model did.
type ModelUsage struct {
	// Names are the model strings the requests actually carried. More than one
	// when a file is served under several ids, which is how llama-swap works.
	Names    []string `json:"names,omitempty" yaml:"names,omitempty"`
	Engines  []string `json:"engines,omitempty" yaml:"engines,omitempty"`
	Requests int      `json:"requests" yaml:"requests"`
	// PromptTokens and OutputTokens are what the engines reported. An engine
	// that reports nothing contributes requests, not tokens.
	PromptTokens uint64    `json:"prompt_tokens,omitempty" yaml:"prompt_tokens,omitempty"`
	OutputTokens uint64    `json:"output_tokens,omitempty" yaml:"output_tokens,omitempty"`
	LastUsed     time.Time `json:"last_used,omitempty" yaml:"last_used,omitempty"`
}

// Usage folds the harbor's rows into usage per request model name. The keys are
// lowercased, because a llama-swap id, an OpenAI request and a file stem differ
// in case and mean one file; the spellings survive in Names.
//
// The window is the caller's: since goes straight to rows.
func Usage(rows RowsFunc, since time.Time) (map[string]ModelUsage, error) {
	out := map[string]ModelUsage{}
	if rows == nil {
		return out, nil
	}
	rs, err := rows(since)
	if err != nil {
		return out, err
	}
	names := map[string]map[string]bool{} // per key: the spellings seen
	kinds := map[string]map[string]bool{} // per key: the engines seen
	for _, r := range rs {
		name := strings.TrimSpace(r.Model)
		if name == "" {
			continue // a request that named no model is nobody's usage
		}
		key := strings.ToLower(name)
		if names[key] == nil {
			names[key], kinds[key] = map[string]bool{}, map[string]bool{}
		}
		u := out[key]
		u.Requests++
		u.PromptTokens += tokens(r.Prompt)
		u.OutputTokens += tokens(r.Output)
		if r.T.After(u.LastUsed) {
			u.LastUsed = r.T
		}
		addName(&u.Names, names[key], name)
		addName(&u.Engines, kinds[key], r.Engine)
		out[key] = u
	}
	return out, nil
}

// tokens is a reported token count a caller can sum: engines report floats, and
// a negative or impossible one is a broken counter, never a subtraction.
func tokens(v float64) uint64 {
	if math.IsNaN(v) || v <= 0 || v > math.MaxUint64 {
		return 0
	}
	return uint64(math.Round(v))
}

// addName records one spelling or one engine once. keep is per list, so a model
// and an engine that share a name stay two facts.
func addName(list *[]string, keep map[string]bool, v string) {
	v = strings.TrimSpace(v)
	if v == "" || keep[v] {
		return
	}
	keep[v] = true
	*list = append(*list, v)
}

// Merge folds another record in: one file served under three ids has one usage.
func (u *ModelUsage) Merge(o ModelUsage) {
	u.Requests += o.Requests
	u.PromptTokens += o.PromptTokens
	u.OutputTokens += o.OutputTokens
	if o.LastUsed.After(u.LastUsed) {
		u.LastUsed = o.LastUsed
	}
	for _, n := range o.Names {
		if !slices.Contains(u.Names, n) {
			u.Names = append(u.Names, n)
		}
	}
	for _, e := range o.Engines {
		if !slices.Contains(u.Engines, e) {
			u.Engines = append(u.Engines, e)
		}
	}
	sort.Strings(u.Names)
	sort.Strings(u.Engines)
}

// SwapIDs maps a llama-swap model id to the GGUF its cmd loads, read from a
// llama-swap config.yaml. An id is what a request names and a path is what a
// file is, and this is the only tie between them that is not a guess. A config
// that is not there, is not readable or is not YAML returns nil, which means
// "no ids", not an error: Berth works without llama-swap.
func SwapIDs(configPath string, env map[string]string) map[string]string {
	raw, err := readCap(configPath, 1<<24)
	if err != nil {
		return nil
	}
	var cfg struct {
		// macros (llama-swap's name) and params (its old name) are the ${name}
		// the cmd strings are written with.
		Macros map[string]string `yaml:"macros"`
		Params map[string]string `yaml:"params"`
		Models map[string]struct {
			Cmd string `yaml:"cmd"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil
	}
	// A config with no macros block is the common case, so build the table
	// rather than write into whichever map yaml left nil.
	macros := map[string]string{}
	for k, v := range cfg.Params {
		macros[k] = v
	}
	for k, v := range cfg.Macros {
		macros[k] = v
	}
	out := map[string]string{}
	for id, m := range cfg.Models {
		p := ModelArg(expand(macroReplacer(macros, env), m.Cmd))
		// A path still holding ${...} is one Berth cannot resolve: the variable
		// lives in the engine's environment, not in this file. An id that names
		// no readable path ties nothing, and saying so is better than matching a
		// path with a dollar sign in it.
		if p == "" || strings.Contains(p, "${") {
			continue
		}
		out[strings.ToLower(id)] = p
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// macroReplacer is a llama-swap cmd's ${name} table: the config's own macros,
// with the environment under them, which is where ${env.MODELS_DIR} comes
// from. env is llama-swap's own environment when Berth could read it (a unit
// sets MODELS_DIR for llama-swap, not for Berth); Berth's own is the fallback.
func macroReplacer(macros, env map[string]string) func(string) string {
	getenv := func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return os.Getenv(k)
	}
	return func(name string) string {
		if strings.HasPrefix(name, "env.") {
			return getenv(strings.TrimPrefix(name, "env."))
		}
		if v, ok := macros[name]; ok {
			return v
		}
		return getenv(name)
	}
}

// expand replaces ${name} until it stops changing: a macro's value is usually
// another variable, so ${ollama}/x becomes ${env.MODELS_DIR}/x becomes
// /srv/models/x. A name nothing resolves stays written as ${name}, and
// the caller skips an id whose path never resolved rather than matching a path
// with a dollar sign in it.
func expand(lookup func(string) string, s string) string {
	for i := 0; i < 5 && strings.Contains(s, "${"); i++ {
		var b strings.Builder
		rest := s
		for {
			j := strings.Index(rest, "${")
			if j < 0 {
				b.WriteString(rest)
				break
			}
			b.WriteString(rest[:j])
			rest = rest[j+2:]
			k := strings.IndexByte(rest, '}')
			if k < 0 {
				b.WriteString("${" + rest)
				break
			}
			name, value := rest[:k], rest[k+1:]
			if v := lookup(name); v != "" {
				b.WriteString(v)
			} else {
				b.WriteString("${" + name + "}")
			}
			rest = value
		}
		if b.String() == s {
			return s // nothing resolved this round: the rest never will
		}
		s = b.String()
	}
	return s
}

// ModelArg is the model file an engine command loads: llama-swap's cmd is a
// whole command line, and -m / --model is the argument that names the file. An
// --mmproj is not it: an id names the model, and a command that loads a
// projector beside it loads the model with -m.
func ModelArg(cmd string) string {
	f := fields(cmd)
	for i := 0; i < len(f); i++ {
		a := f[i]
		if (a == "-m" || a == "--model") && i+1 < len(f) {
			return f[i+1]
		}
		if eq := "--model="; strings.HasPrefix(a, eq) {
			return strings.TrimPrefix(a, eq)
		}
	}
	// Engines that take the model as a plain argument (NInfer's serve binary):
	// the first argument that is a GGUF file.
	// Only a true positional argument: a .gguf right after a flag is that
	// flag's value (--mmproj, --model-draft), not the model.
	for i, a := range f {
		if strings.HasSuffix(strings.ToLower(a), ".gguf") && (i == 0 || !strings.HasPrefix(f[i-1], "-")) {
			return a
		}
	}
	return ""
}

// fields splits a command line the way a shell would for the one thing Berth
// needs: quotes hold a path with a space in it, and the quotes themselves go.
// No escapes and no substitution - a llama-swap cmd is a plain command line.
func fields(s string) []string {
	var out []string
	var cur []rune
	var quote rune
	fresh := true
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
				continue
			}
			cur = append(cur, r)
		case r == '\'' || r == '"':
			quote, fresh = r, false
		case r == ' ' || r == '\t' || r == '\n':
			if !fresh {
				out = append(out, string(cur))
				cur, fresh = nil, true
			}
		default:
			cur, fresh = append(cur, r), false
		}
	}
	if !fresh {
		out = append(out, string(cur))
	}
	return out
}

// MatchUsage ties the request log to the files on disk and attaches each
// model's usage to it, as Model.Usage. It returns the request names that matched
// no file: a model deleted since, or one another machine served.
//
// A name matches a model, case-insensitively, in this order of confidence: a
// llama-swap id whose cmd loads one of the model's files; the file's stem
// exactly; the stem inside the name or the name inside the stem. The last rule
// is deliberately loose, so when several files claim one name the longest stem
// wins - the more specific name is the one that was meant.
func MatchUsage(ms []Model, usage map[string]ModelUsage, swapIDs map[string]string) map[string]ModelUsage {
	orphan := map[string]ModelUsage{}
	for _, key := range sortedUsageKeys(usage) {
		best := -1
		bestScore, bestLen := 0, 0
		for i := range ms {
			s := matchScore(ms[i], key, swapIDs)
			if s == 0 {
				continue
			}
			switch {
			case s > bestScore:
			case s == bestScore && len(ms[i].Name) > bestLen:
			case s == bestScore && len(ms[i].Name) == bestLen && ms[i].Path < ms[best].Path:
			default:
				continue
			}
			best, bestScore, bestLen = i, s, len(ms[i].Name)
		}
		if best < 0 {
			orphan[key] = usage[key]
			continue
		}
		if ms[best].Usage == nil {
			ms[best].Usage = &ModelUsage{}
		}
		ms[best].Usage.Merge(usage[key])
	}
	return orphan
}

// matchScore is how sure a request name is this model: 3 when a llama-swap id
// loads one of its files, 2 when the stem is the name, 1 when one contains the
// other, 0 when neither.
func matchScore(m Model, key string, swapIDs map[string]string) int {
	if p, ok := swapIDs[key]; ok {
		for _, f := range m.files() {
			if samePath(p, f) {
				return 3
			}
		}
	}
	stem := strings.ToLower(m.Name)
	if stem == "" {
		return 0
	}
	if stem == key {
		return 2
	}
	if strings.Contains(key, stem) || strings.Contains(stem, key) {
		return 1
	}
	return 0
}

// samePath is llama-swap's path and Berth's path meaning one file, however each
// spelled the directories: one is a suffix of the other, at a separator.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	long, short := a, b
	if len(short) > len(long) {
		long, short = short, long
	}
	return strings.HasSuffix(long, string(filepath.Separator)+short)
}

// files is every file a model is: a split model's id may name any part, and a
// companion a request names is still one of this model's files.
func (m Model) files() []string {
	if len(m.Files) > 0 {
		return m.Files
	}
	if m.Path != "" {
		return []string{m.Path}
	}
	return nil
}

// readCap is os.ReadFile with a size limit: a config is a file someone edits,
// and anything past a megabyte of it is not a config.
func readCap(path string, max int64) ([]byte, error) {
	if path == "" {
		return nil, os.ErrNotExist
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() || fi.Size() > max {
		return nil, os.ErrInvalid
	}
	return os.ReadFile(path)
}

func sortedUsageKeys(m map[string]ModelUsage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
