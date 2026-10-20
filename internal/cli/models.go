// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/models"
)

// modelsDoc is what `berth models --format json|yaml` prints: the roots that
// were read, every model found, and the totals a person would add up.
//
// Models are the listing the human output shows - main models and embeddings,
// with their vision projectors and drafts counted in +COMP - unless --all asked
// for every file. Bytes and Files are always the whole library.
type modelsDoc struct {
	Status     string                 `json:"status" yaml:"status"` // ok | failed
	Roots      []string               `json:"roots" yaml:"roots"`
	Count      int                    `json:"count" yaml:"count"` // rows of Models
	Bytes      uint64                 `json:"bytes" yaml:"bytes"` // every file in every root
	Files      int                    `json:"files" yaml:"files"`
	Companions int                    `json:"companions" yaml:"companions"` // mmproj/draft files, not rows unless --all
	Duplicates int                    `json:"duplicates" yaml:"duplicates"` // models likely the same file twice
	All        bool                   `json:"all,omitempty" yaml:"all,omitempty"`
	Shelf      string                 `json:"shelf,omitempty" yaml:"shelf,omitempty"` // the --shelf filter, when set
	Shelves    map[string]*shelfUsage `json:"shelves,omitempty" yaml:"shelves,omitempty"`

	// Usage is the window --usage asked about; empty when the column is the
	// only reason the harbor log was read.
	Since     string         `json:"usage_since,omitempty" yaml:"usage_since,omitempty"`
	DB        string         `json:"usage_db,omitempty" yaml:"usage_db,omitempty"`
	Unmatched []servedDoc    `json:"served but not on disk,omitempty" yaml:"served but not on disk,omitempty"`
	Models    []models.Model `json:"models" yaml:"models"`
	Errors    []modelErrDoc  `json:"errors,omitempty" yaml:"errors,omitempty"`
	// CacheChecked says whether the CACHED column was measured. Without it a
	// dash in the column is a skipped check, not a cold model.
	CacheChecked bool `json:"cache_checked" yaml:"cache_checked"`
}

// servedDoc is one model name the harbor served that no file on disk answers to
// - a model since deleted, or one served by an engine on another machine.
type servedDoc struct {
	Model        string    `json:"model" yaml:"model"`
	Engines      []string  `json:"engines,omitempty" yaml:"engines,omitempty"`
	Requests     int       `json:"requests" yaml:"requests"`
	PromptTokens uint64    `json:"prompt_tokens,omitempty" yaml:"prompt_tokens,omitempty"`
	OutputTokens uint64    `json:"output_tokens,omitempty" yaml:"output_tokens,omitempty"`
	LastUsed     time.Time `json:"last_used,omitempty" yaml:"last_used,omitempty"`
}

// modelErrDoc is one model's read failure. The scan keeps going: a
// half-downloaded file is a fact about that model, not a failed listing.
type modelErrDoc struct {
	Name string `json:"name" yaml:"name"`
	Path string `json:"path" yaml:"path"`
	Err  string `json:"err" yaml:"err"`
}

type modelsListView struct{ doc modelsDoc }

func (v modelsListView) Data() any { return v.doc }

func (v modelsListView) Human(c *Context, ok bool) error {
	d := v.doc
	if len(d.Models) == 0 {
		what := "No models found"
		if d.Shelf != "" {
			what = fmt.Sprintf("No models on the %s shelf", d.Shelf)
		}
		c.Printf("%s %s\n", c.Style.OK(markOK), what)
		for _, r := range d.Roots {
			c.Bullet("Nothing under %s", r)
		}
		for _, e := range d.Errors {
			c.Bullet("%s: %s", e.Name, e.Err)
		}
		return nil
	}
	what := "model"
	if d.Count != 1 {
		what = "models"
	}
	since := ""
	if d.Since != "" {
		since = fmt.Sprintf(", used since %s", d.Since)
	}
	c.Printf("%s %d %s%s\n", c.Style.OK(markOK), d.Count, what, since)

	head := []string{"MODEL", "SHELF", "QUANT", "SIZE", "CACHED", "PARAMS", "CTX", "ARCH", "+COMP", "USED"}
	rows := make([][]string, 0, len(d.Models))
	for _, m := range d.Models {
		rows = append(rows, modelRow(m, d.Since != "", d.CacheChecked))
	}
	if d.Since != "" {
		head = append(head, "REQ", "TOKENS")
	}
	table(c, head, rows)

	for _, e := range d.Errors {
		c.Bullet("%s: %s", e.Name, c.Style.Err(e.Err))
	}
	// The total reads as one more row of the list it sums, so it goes under it
	// at the same indent rather than as a second verdict.
	pad := strings.Repeat(" ", len(markOK)+1)
	c.Printf("\n%stotal %s, %d file%s in %d root%s\n",
		pad, gib(d.Bytes), d.Files, plural(d.Files), len(d.Roots), plural(len(d.Roots)))
	if len(d.Shelves) > 0 {
		order := make([]string, 0, len(d.Shelves))
		for s := range d.Shelves {
			order = append(order, s)
		}
		sort.Strings(order)
		line := "by shelf:"
		for _, s := range order {
			u := d.Shelves[s]
			label := s
			if label == "" {
				label = "unshelved"
			}
			line += fmt.Sprintf(" %s %s in %d model%s", label, gib(u.Bytes), u.Models, plural(u.Models))
		}
		c.Printf("%s%s\n", pad, line)
	}
	if d.Companions > 0 && !d.All {
		c.Printf("%s%d companion file%s (vision projector, draft) - %s lists all %d\n", pad,
			d.Companions, plural(d.Companions), c.Style.Dim("berth models --all"), d.Count+d.Companions)
	}
	if d.Duplicates > 0 {
		c.Printf("%s%d model%s look like the same file twice\n",
			pad, d.Duplicates, plural(d.Duplicates))
	}
	return nil
}

// modelRow is one listing row. tabwriter pads it, so a 70-character filename
// pushes the row wider than the header rather than staggering the columns.
func modelRow(m models.Model, usage, cache bool) []string {
	quant := dash(m.Quant)
	if m.Parts > 1 {
		quant = fmt.Sprintf("%s/%d", quant, m.Parts)
	}
	if m.Role != "" && m.Role != models.RoleModel && m.Role != models.RoleEmbedding {
		quant = quant + "/" + m.Role // --all: what this file is for
	}
	row := []string{
		truncate(m.Name, nameCol),
		dashShelf(m.Shelf),
		quant,
		size(m.Bytes),
		cachedCell(m, cache),
		params(m.Params),
		count(m.Context),
		dash(m.Arch),
		count(uint64(len(m.Companions))),
		ago(usedWhen(m.Usage)),
	}
	if usage {
		row = append(row, count(uint64(requests(m.Usage))), tokens(m.Usage))
	}
	return row
}

// cachedCell is a model's CACHED column: its resident share with the tier it
// reads as, or a dash when the check was skipped or measured nothing. The
// share is of the bytes in the page cache, which the kernel takes back at
// will, so it says nothing about RAM held.
func cachedCell(m models.Model, checked bool) string {
	if !checked {
		return "-"
	}
	share := m.CachedShare()
	if share < 0 {
		return "-"
	}
	return fmt.Sprintf("%d%% %s", share, m.Warmth())
}

// A model with no harbor row has never been seen serving anything.
func usedWhen(u *models.ModelUsage) time.Time {
	if u == nil {
		return time.Time{}
	}
	return u.LastUsed
}

func requests(u *models.ModelUsage) int {
	if u == nil {
		return 0
	}
	return u.Requests
}

// tokens is a model's prompt+output total in the compact units the ledger uses.
func tokens(u *models.ModelUsage) string {
	if u == nil {
		return "-"
	}
	return tokensOf(u.PromptTokens + u.OutputTokens)
}

// tokensOf is a token count the way a dashboard writes it: 12k, 4.3M.
func tokensOf(n uint64) string {
	switch {
	case n == 0:
		return "-"
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// nameCol is the listing's widest name; longer stems are cut with an ellipsis
// and still resolve with `berth models show <path>`.
const nameCol = 60

// table prints rows under header with tabwriter, so the columns line up however
// wide a cell turns out to be.
func table(c *Context, header []string, rows [][]string) {
	w := tabwriter.NewWriter(c.Out, 0, 0, 2, ' ', tabwriter.TabIndent)
	fmt.Fprintln(w, strings.Join(header, "\t"))
	for _, r := range rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	_ = w.Flush()
}

// ago is when a model last worked, the way a person reads a date: 12d ago,
// never. A future stamp is a clock that moved, and reads as now.
func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
	}
	return fmt.Sprintf("%dw ago", int(d.Hours()/24/7))
}

// size is a file's size in the unit that reads it: 481 MiB, 14.6 GiB. The label
// is the one the division that stopped the loop actually landed on, which is the
// difference between a 20 GiB model and one reported as 18 TiB.
func size(b uint64) string {
	const unit = 1 << 10
	if b == 0 {
		return "-"
	}
	f, u := float64(b), "B"
	for _, next := range []string{"KiB", "MiB", "GiB", "TiB"} {
		if f < unit {
			break
		}
		f, u = f/unit, next
	}
	switch {
	case f < 10:
		return fmt.Sprintf("%.2f %s", f, u)
	case f < 100:
		return fmt.Sprintf("%.1f %s", f, u)
	}
	return fmt.Sprintf("%.0f %s", f, u)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// dashShelf is a model's shelf, dashed when no evidence names one.
func dashShelf(s models.Shelf) string {
	if s == "" {
		return "-"
	}
	return string(s)
}

// gib is a file size as a person reads a model's size.
func gib(b uint64) string { return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30)) }

// params is a parameter count in the units a model is sold in.
func params(n uint64) string {
	switch {
	case n == 0:
		return "-"
	case n >= 1_000_000_000_000:
		return fmt.Sprintf("%.1fT", float64(n)/1e12)
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%d", n)
}

// count is a metadata number, dashed when the file states none.
func count(n uint64) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", n)
}

func runModels(c *Context) int {
	// Two subcommands take a name, and both may come with the same global flags.
	if len(c.Args) > 0 {
		switch c.Args[0] {
		case "show":
			c.Args = c.Args[1:]
			return runModelsShow(c)
		case "export":
			c.Args = c.Args[1:]
			return runModelsExport(c)
		}
	}
	var dirs stringList
	var usageWin string
	var all, noUsage bool
	fs := c.FlagSet()
	fs.Var(&dirs, "dir", "a directory to scan, repeatable (default: the Hugging Face cache, "+
		"LM Studio's model directories, Ollama's store and ~/models)")
	fs.BoolVar(&all, "all", false, "list every file: vision projectors and drafts are counted, "+
		"not listed, without this")
	fs.StringVar(&usageWin, "usage", "", "add REQ and TOKENS columns over a window: 30d, 24h, 90m "+
		"(the USED column always shows the last time the harbor saw a model serve anything)")
	fs.BoolVar(&noUsage, "no-usage", false, "never open the harbor database")
	noCache := fs.Bool("no-cache-check", false, "skip the page-cache check behind the CACHED column")
	shelf := fs.String("shelf", "", "show only models on this shelf: chat, image or audio")
	if code, done := c.Parse(fs); done {
		return code
	}
	if *shelf != "" && !models.Shelf(*shelf).Valid() {
		return c.fail(Failf("--shelf %q is not a shelf", *shelf).
			WithHint("the shelves are chat, image and audio"))
	}
	if code, done := c.noArgs(); done {
		return code
	}
	doc := modelsDoc{Status: "ok", Shelf: *shelf}
	// The window is what a person types; 30d because that is how a retention is
	// written everywhere else in Berth.
	var since time.Time
	if usageWin != "" {
		d, err := parseWindow(usageWin)
		if err != nil {
			return c.fail(Failf("bad --usage %q: %v", usageWin, err).
				WithHint("name a window like 7d, 30d, 24h or 90m"))
		}
		since, doc.Since = time.Now().Add(-d), humanWindow(d)
	}
	// --dir is the command line's say; without it the config's [models] dirs
	// apply, and without those the default roots do.
	cfg, _, _, e := loadConfig(c, config.DefaultPath())
	if e != nil {
		return c.fail(e)
	}
	roots := dirs.vals
	if len(roots) == 0 {
		roots = cfg.Models.Dirs
	}
	// A directory that is not there is a warning, because the other roots still
	// read; when none of them are, the command has nothing to report.
	missing := models.MissingRoots(roots)
	if len(roots) > 0 && len(missing) == len(roots) {
		return c.fail(Systemf("no such model directory: %s", strings.Join(missing, ", ")).
			WithHint("run \"berth models\" to scan the default roots instead"))
	}
	if !c.Machine() {
		for _, d := range missing {
			c.Warnf("%s is not a directory: nothing was scanned there", d)
		}
	}
	ms := models.Scan(roots)
	scanned := roots
	if len(scanned) == 0 {
		scanned = models.DefaultRoots()
	}
	doc.Roots, doc.All = scanned, all
	// The config's declaration of a root beats what the files themselves say.
	var declared []models.RootShelf
	for _, r := range cfg.Library.Root {
		if r.Shelf != "" {
			declared = append(declared, models.RootShelf{Path: r.Path, Shelf: models.Shelf(r.Shelf)})
		}
	}
	models.ApplyRootShelves(ms, declared)
	// The scan's snapshot goes back into each root it came from: a sibling
	// boot reading the same root sees these resolutions, not just its own scan.
	for _, root := range scanned {
		var rm []models.Model
		for _, m := range ms {
			if underPath(m.Path, root) {
				rm = append(rm, m)
			}
		}
		_ = models.WriteCatalog(root, rm) // best effort: a read-only root keeps its old one
	}
	if !noUsage {
		doc.DB = defaultHarborDB()
		attachUsage(c, ms, swapIDs(cfg), since, &doc)
	}
	// The listing is the library a person runs: main models and embeddings. The
	// totals below always cover every file, companions included.
	listed := ms
	if !all {
		listed = models.Main(ms)
		seen := map[string]bool{}
		for _, m := range ms {
			for _, c := range m.Companions {
				seen[c] = true // one projector shared by three quants is one file
			}
		}
		doc.Companions = len(seen)
	}
	if *shelf != "" {
		var filtered []models.Model
		for _, m := range listed {
			if m.Shelf == models.Shelf(*shelf) {
				filtered = append(filtered, m)
			}
		}
		listed = filtered
	}
	doc.Count = len(listed)
	// The CACHED column measures the models the list shows, at most four at
	// a time, reading the kernel's page table: no file byte is touched and
	// no page is pulled in.
	if !*noCache {
		models.Cache(listed)
	}
	doc.CacheChecked = !*noCache
	doc.Models = listed
	for _, m := range ms {
		doc.Bytes += m.Bytes
		if len(m.Files) > 0 {
			doc.Files += len(m.Files)
		} else {
			doc.Files++ // a model is at least one file, even one that failed to read
		}
		if m.Err != "" {
			doc.Errors = append(doc.Errors, modelErrDoc{Name: m.Name, Path: m.Path, Err: m.Err})
		}
		if doc.Shelves == nil {
			doc.Shelves = map[string]*shelfUsage{}
		}
		u := doc.Shelves[string(m.Shelf)]
		if u == nil {
			u = &shelfUsage{}
			doc.Shelves[string(m.Shelf)] = u
		}
		u.Bytes += m.Bytes
		u.Models++
	}
	for _, g := range models.Duplicates(ms) {
		doc.Duplicates += len(g)
	}
	// A model that would not read is a failed listing: the report says which,
	// and the exit code says it again for a script.
	var failure *Error
	if len(doc.Errors) > 0 {
		doc.Status = "failed"
		failure = Failf("%d of %d models could not be read", len(doc.Errors), doc.Count)
		for _, e := range doc.Errors {
			failure.Detail("%s: %s", e.Name, e.Err)
		}
	}
	return c.Write(modelsListView{doc: doc}, failure)
}

// modelDoc is what `berth models show --format json|yaml` prints: one model in
// full, with the header metadata that has no column in the listing.
type modelDoc struct {
	Status string       `json:"status" yaml:"status"` // ok | failed
	Path   string       `json:"path" yaml:"path"`
	Model  models.Model `json:"model" yaml:"model"`
	// The header detail, when the model is a GGUF.
	Heads    uint64            `json:"heads_kv,omitempty" yaml:"heads_kv,omitempty"`
	Embed    uint64            `json:"embedding_length,omitempty" yaml:"embedding_length,omitempty"`
	Tensors  map[uint32]uint64 `json:"tensor_types,omitempty" yaml:"tensor_types,omitempty"`
	Meta     map[string]any    `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Template string            `json:"chat_template,omitempty" yaml:"chat_template,omitempty"`
	Err      string            `json:"err,omitempty" yaml:"err,omitempty"`
}

// hiddenMeta is the metadata a report never prints in full: a vocab is a file
// of a hundred thousand strings, and its length is in the document.
var hiddenMeta = map[string]bool{
	"tokenizer.ggml.tokens":       true,
	"tokenizer.ggml.scores":       true,
	"tokenizer.ggml.merges":       true,
	"tokenizer.ggml.token_type":   true,
	"tokenizer.ggml.added_tokens": true,
}

type modelShowView struct{ doc modelDoc }

func (v modelShowView) Data() any { return v.doc }

func (v modelShowView) Human(c *Context, ok bool) error {
	d := v.doc
	m := d.Model
	if ok {
		c.Printf("%s %s\n", c.Style.OK(markOK), c.Style.Bold(m.Name))
	} else {
		c.Printf("%s %s\n", c.Style.Err(markError), c.Style.Bold(m.Name))
	}
	// One model has no table to align, so its row prints as one field per word.
	c.Sub("%s", strings.Join(modelRow(m, false, true), "  "))
	c.Sub("title: %s", dash(m.Title))
	if m.Shelf != "" {
		c.Sub("shelf: %s", m.Shelf)
	}
	if m.Role != "" {
		c.Sub("role: %s", m.Role)
	}
	c.Sub("path: %s", m.Path)
	for _, comp := range m.Companions {
		c.Sub("companion: %s", comp)
	}
	if len(m.Files) > 1 {
		c.Sub("files (%d):", len(m.Files))
		for _, f := range m.Files {
			c.Printf("%s%s\n", subIndent, f)
		}
	}
	if m.Err != "" {
		c.Sub("%s", c.Style.Err(m.Err))
	}
	if m.CachedSize > 0 {
		c.Sub("cached: %s of %s in the page cache (%d%%, %s)",
			size(m.CachedBytes), size(m.CachedSize), m.CachedShare(), m.Warmth())
	}
	if d.Heads > 0 {
		c.Sub("kv heads: %d", d.Heads)
	}
	if d.Embed > 0 {
		c.Sub("embedding length: %d", d.Embed)
	}
	if len(d.Tensors) > 0 {
		var parts []string
		for _, t := range sortedTypes(d.Tensors) {
			parts = append(parts, fmt.Sprintf("%s x%d", models.TypeName(t), d.Tensors[t]))
		}
		c.Sub("tensors: %s", strings.Join(parts, ", "))
	}
	for _, k := range sortedKeys(d.Meta) {
		if hiddenMeta[k] {
			continue
		}
		c.Sub("%s: %s", k, metaValue(d.Meta[k]))
	}
	if d.Template != "" {
		c.Sub("chat template: %s", firstLine(d.Template))
	}
	// The usage block is the harbor's answer, not the file's: what it did, when
	// it last did it, and under which names a client asked for it.
	if u := m.Usage; u != nil && u.Requests > 0 {
		c.Sub("usage: %d request%s, %s prompt + %s output tokens, last used %s",
			u.Requests, plural(u.Requests), tokensOf(u.PromptTokens), tokensOf(u.OutputTokens), ago(u.LastUsed))
		if len(u.Engines) > 0 {
			c.Sub("engines: %s", strings.Join(u.Engines, ", "))
		}
		if len(u.Names) > 0 {
			c.Sub("served as: %s", strings.Join(u.Names, ", "))
		}
	} else {
		c.Sub("usage: %s", c.Style.Dim("nothing in the harbor log has served this model"))
	}
	if d.Err != "" {
		c.Sub("%s", c.Style.Err(d.Err))
	}
	return nil
}

func runModelsShow(c *Context) int {
	var dirs stringList
	fs := c.FlagSet()
	fs.Var(&dirs, "dir", "where to look for a name, repeatable (default: berth.toml's [models] dirs)")
	// A person types the name first and the flags after, and the flag package
	// stops at the first bare word, so the name is taken from wherever it is.
	if code, done := c.Parse(fs); done {
		return code
	}
	arg := ""
	if len(c.Args) > 0 {
		arg, c.Args = c.Args[0], c.Args[1:]
		if code, done := c.Parse(fs); done {
			return code
		}
	}
	if len(c.Args) > 0 {
		return c.fail(Systemf("unexpected argument %q", c.Args[0]))
	}
	if arg == "" {
		return c.fail(Systemf("models show needs a model name or a path").
			WithHint("run \"berth models\" for the list"))
	}
	cfg, _, _, e := loadConfig(c, config.DefaultPath())
	if e != nil {
		return c.fail(e)
	}
	roots := dirs.vals
	if len(roots) == 0 {
		roots = cfg.Models.Dirs
	}
	m, hdr, err := showModel(arg, roots)
	doc := modelDoc{Status: "ok", Path: arg, Model: m}
	if err == nil {
		// One model's usage is the listing's, taken over the whole log: the
		// question `models show` answers is when this file last worked.
		ms := []models.Model{m}
		attachUsage(c, ms, swapIDs(cfg), time.Time{}, &modelsDoc{})
		models.Cache(ms)
		m, doc.Model = ms[0], ms[0]
	}
	if err != nil {
		if m.Name == "" {
			m.Name, m.Path = filepath.Base(arg), arg
			doc.Model = m
		}
		doc.Status, doc.Err = "failed", err.Error()
		return c.Write(modelShowView{doc: doc}, Failf("%s: %v", arg, err))
	}
	doc.Model = m
	if hdr != nil {
		doc.Heads, doc.Embed = hdr.HeadsKV(), hdr.EmbeddingLength()
		doc.Tensors, doc.Meta = hdr.TypeCounts, hdr.Meta
		doc.Template = hdr.ChatTemplate()
	}
	return c.Write(modelShowView{doc: doc}, nil)
}

// showModel resolves what the argument means: a path that exists, else a name
// from the library listing - the listing's name (the file's stem), the name its
// header gave it, or the file itself. A name matching two models is not guessed
// at. roots is what the listing would have scanned: empty means the defaults.
func showModel(arg string, roots []string) (models.Model, *models.GGUF, error) {
	if fi, err := os.Stat(arg); err == nil {
		return pathModel(arg, fi)
	}
	var matches []models.Model
	for _, m := range models.Scan(roots) {
		if m.Name == arg || m.Title == arg || filepath.Base(m.Path) == arg {
			matches = append(matches, m)
		}
	}
	switch len(matches) {
	case 0:
		return models.Model{}, nil, fmt.Errorf("no model named %q and no such file or directory", arg)
	case 1:
		m := matches[0]
		// Only a GGUF has a header to show; a safetensors directory and an
		// Ollama tag report what the scan already knows.
		g, err := models.ReadGGUF(m.Path)
		if err != nil {
			return m, nil, nil
		}
		return m, g, nil
	}
	var where []string
	for _, m := range matches {
		where = append(where, m.Path)
	}
	return models.Model{}, nil, fmt.Errorf("%d models are named %q, show one by path (%s)",
		len(matches), arg, strings.Join(where, ", "))
}

// siblings is the vision projector and draft that belong to a file named by
// path: the same rule the listing uses, which is why the directory is scanned
// rather than guessed at. A directory that will not scan has no companions.
func siblings(p string) []string {
	dir := filepath.Dir(p)
	for _, m := range models.Scan([]string{dir}) {
		if m.Path == p {
			return m.Companions
		}
	}
	return nil
}

// pathModel is a path that exists: a GGUF read here, or whatever the scan makes
// of a directory.
func pathModel(p string, fi os.FileInfo) (models.Model, *models.GGUF, error) {
	if !fi.IsDir() && strings.HasSuffix(p, ".gguf") {
		g, err := models.ReadGGUF(p)
		if err != nil {
			return models.Model{}, nil, err
		}
		m := models.Model{
			Name: models.Stem(p), Title: g.Name(), Path: p, Files: []string{p},
			Bytes: uint64(fi.Size()), Format: models.FormatGGUF, Source: models.SourceDir,
			Parts: g.SplitCount(), Role: models.RoleOf(g.Arch(), p),
			Arch: g.Arch(), Quant: g.FileType(), Params: g.Params,
			Context: g.ContextLength(), Layers: g.Layers(), ModTime: fi.ModTime(),
		}
		if m.Parts == 0 {
			m.Parts = 1
		}
		m.Companions = siblings(p)
		return m, g, nil
	}
	// A directory is scanned the way the listing would scan it, so a path and a
	// name say the same thing about the same model.
	ms := models.Scan([]string{p})
	switch len(ms) {
	case 0:
		return models.Model{}, nil, fmt.Errorf("no model in %s", p)
	case 1:
		return ms[0], nil, nil
	}
	for _, m := range ms {
		if m.Path == p || len(m.Files) == 1 && m.Files[0] == p {
			return m, nil, nil
		}
	}
	return models.Model{}, nil, fmt.Errorf("%s holds %d models, name one of them", p, len(ms))
}

// firstLine is a chat template's opening: the whole template is a document
// field, and one line of it stays one line here.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		return truncate(s[:i], 200) + "…"
	}
	return truncate(s, 200)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// stringList is a repeatable flag: --dir A --dir B scans two roots.
type stringList struct{ vals []string }

func (s *stringList) String() string { return strings.Join(s.vals, string(filepath.ListSeparator)) }

func (s *stringList) Set(v string) error {
	s.vals = append(s.vals, v)
	return nil
}

// sortedKeys is a map's keys in the order a report needs them to stay the same
// twice in a row.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedTypes is the same for a GGUF's tensor-type counts, whose keys are the
// numbers the header uses.
func sortedTypes(m map[uint32]uint64) []uint32 {
	out := make([]uint32, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// metaValue renders one header value for people: a GGUF array as its members,
// or its length when only that was kept (the tokenizer's vocabulary).
func metaValue(v any) string {
	a, ok := v.(models.Array)
	if !ok {
		if p, isPtr := v.(*models.Array); isPtr && p != nil {
			a, ok = *p, true
		}
	}
	if !ok {
		return fmt.Sprint(v)
	}
	if a.Vals == nil {
		return fmt.Sprintf("[%d items]", a.Len)
	}
	parts := make([]string, len(a.Vals))
	for i, x := range a.Vals {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
