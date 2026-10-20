package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/models"
	"github.com/T-Crypt/berth/internal/suite"
)

// engineSuiteDeps is the suite seam the package tests replace: in production
// a Manager over the system git and shell, in a test one over fakes.
var engineSuiteDeps = func(log *slog.Logger) suite.Manager {
	return suite.NewManager(log)
}

// loadSuiteState reads the suite state file for the berth.toml at cfgPath.
func loadSuiteState(cfgPath string) (suite.State, *Error) {
	st, err := suite.Load(suite.SuitePath(cfgPath))
	if err != nil {
		return suite.State{}, Systemf("suite state unreadable: %s", suite.SuitePath(cfgPath)).Detail("%v", err)
	}
	return st, nil
}

// findInstallIndex names one install in the state.
func findInstallIndex(st suite.State, name string) int {
	for i := range st.Installs {
		if st.Installs[i].Name == name {
			return i
		}
	}
	return -1
}

// suiteModelRoots is the union an install's extra_model_paths.yaml gets:
// the [models] dirs, the [library] roots and the usual stores, in that order.
func suiteModelRoots(cfg config.Config) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, d := range cfg.Models.Dirs {
		add(d)
	}
	for _, r := range cfg.Library.Root {
		add(r.Path)
	}
	for _, d := range models.DefaultRoots() {
		add(d)
	}
	return out
}

// queueRunning reads the install's /queue. An install that is down has an
// empty queue, not an error: a down or idle install is fine to touch. An
// install that answers but not with a queue is refused: Berth never touches
// a server it cannot read.
func queueRunning(inst suite.Install) (int, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/queue", inst.Port))
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("/queue answered %d", resp.StatusCode)
	}
	var q struct {
		Running []json.RawMessage `json:"queue_running"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return 0, fmt.Errorf("/queue: %w", err)
	}
	return len(q.Running), nil
}

// suiteComponentDoc is one component of an install in the machine documents.
type suiteComponentDoc struct {
	Name     string `json:"name" yaml:"name"`
	Path     string `json:"path,omitempty" yaml:"path,omitempty"`
	Repo     string `json:"repo,omitempty" yaml:"repo,omitempty"`
	Channel  string `json:"channel,omitempty" yaml:"channel,omitempty"`
	Commit   string `json:"commit" yaml:"commit"`
	Previous string `json:"previous,omitempty" yaml:"previous,omitempty"`
	Dirty    bool   `json:"dirty,omitempty" yaml:"dirty,omitempty"`
}

func componentDocs(in suite.Install) []suiteComponentDoc {
	out := make([]suiteComponentDoc, 0, len(in.Components))
	for _, cp := range in.Components {
		out = append(out, suiteComponentDoc{Name: cp.Name, Path: cp.Path, Repo: cp.Repo,
			Channel: cp.Channel, Commit: cp.Commit, Previous: cp.Previous, Dirty: cp.Dirty})
	}
	return out
}

// suiteInstallDoc is one install in the machine documents.
type suiteInstallDoc struct {
	Name       string              `json:"name" yaml:"name"`
	Kind       string              `json:"kind" yaml:"kind"`
	Path       string              `json:"path" yaml:"path"`
	Port       int                 `json:"port" yaml:"port"`
	Priority   string              `json:"priority" yaml:"priority"`
	KeepUp     bool                `json:"keep_up,omitempty" yaml:"keep_up,omitempty"`
	Venv       string              `json:"venv,omitempty" yaml:"venv,omitempty"`
	Components []suiteComponentDoc `json:"components" yaml:"components"`
}

func installDocs(st suite.State) []suiteInstallDoc {
	out := make([]suiteInstallDoc, 0, len(st.Installs))
	for _, in := range st.Installs {
		out = append(out, suiteInstallDoc{Name: in.Name, Kind: in.Kind, Path: in.Path, Port: in.Port,
			Priority: in.Priority, KeepUp: in.KeepUp, Venv: in.Venv, Components: componentDocs(in)})
	}
	return out
}

// ---------- install ----------

// engineInstallView is `berth engine install`: the install as the suite
// records it and the start line the daemon and gateway derive from it.
type engineInstallView struct {
	doc   suiteInstallDoc
	start []string
}

func (v engineInstallView) Data() any {
	return struct {
		suiteInstallDoc
		Start []string `json:"start" yaml:"start"`
	}{v.doc, v.start}
}

func (v engineInstallView) Human(c *Context, ok bool) error {
	c.Printf("%s %s at %s (port %d, priority %s)\n", c.Style.OK(markOK), v.doc.Name, v.doc.Path, v.doc.Port, v.doc.Priority)
	for _, cp := range v.doc.Components {
		line := fmt.Sprintf("%s @ %s", cp.Name, short(cp.Commit))
		if cp.Previous != "" {
			line += fmt.Sprintf(" (previous %s)", short(cp.Previous))
		}
		if cp.Dirty {
			line += " [dirty]"
		}
		c.Bullet("%s", line)
	}
	c.Bullet("start: %s", strings.Join(v.start, " "))
	c.Bullet("the daemon and gateway pick the install up at their next start")
	return nil
}

func runEngineInstall(c *Context) int {
	fs := c.FlagSet()
	cfgPath := fs.String("config", config.DefaultPath(), "berth.toml ($BERTH_CONFIG); the model roots come from it")
	adopt := fs.String("adopt", "", "adopt the existing ComfyUI checkout at PATH in place")
	fresh := fs.String("path", "", "clone a fresh install into PATH (default: $XDG_DATA_HOME/berth/comfyui/NAME)")
	port := fs.Int("port", 8188, "the port the ComfyUI server listens on")
	prio := fs.String("priority", "batch", "batch, interactive or foreground")
	keepUp := fs.Bool("keep-up", false, "on drain: unload and leave the server up")
	channel := fs.String("channel", "", "the remote ref update follows (a fresh install defaults to master)")
	repo := fs.String("repo", "", "the core repo a fresh install clones (default: ComfyUI)")
	var packs stringList
	fs.Var(&packs, "pack", "a node pack a fresh install clones into custom_nodes (repeatable)")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("berth engine install NAME takes exactly one argument").
			WithHint("usage: berth engine install NAME [--adopt PATH | --path PATH] [flags]"))
	}
	name := c.Args[0]
	if *adopt != "" && *fresh != "" {
		return c.fail(Failf("--adopt and --path are mutually exclusive: name the checkout to adopt or the directory to clone into"))
	}
	cfg, warns, _, e := loadConfig(c, *cfgPath)
	if e != nil {
		return c.fail(e)
	}
	if !c.Machine() {
		reportWarnings(c, warns)
	}
	st, se := loadSuiteState(*cfgPath)
	if se != nil {
		return c.fail(se)
	}
	m := engineSuiteDeps(c.Log())
	var inst suite.Install
	var err error
	if *adopt != "" {
		inst, err = m.Adopt(name, *adopt, *port, *prio, *keepUp)
		if err != nil {
			if errors.Is(err, suite.ErrNotACheckout) {
				return c.fail(Failf("%v", err))
			}
			return c.fail(Systemf("%v", err))
		}
	} else {
		inst, err = m.Install(name, suite.InstallOpts{Path: *fresh, Repo: *repo, Channel: *channel,
			Port: *port, Priority: *prio, KeepUp: *keepUp, Packs: packs.vals})
		if err != nil {
			return c.fail(Systemf("%v", err))
		}
	}
	// the new install must fit the state: name and port are unique, and the
	// state stays valid as a whole
	candidate := suite.State{Installs: append(append([]suite.Install{}, st.Installs...), inst)}
	if verr := candidate.Validate(); verr != nil {
		return c.fail(Failf("install %q refused: %v", name, verr))
	}
	if err := suite.WriteModelPaths(inst, suiteModelRoots(cfg)); err != nil {
		return c.fail(Systemf("write the model roots into %s: %v", inst.Path, err))
	}
	if err := suite.Save(suite.SuitePath(*cfgPath), candidate); err != nil {
		return c.fail(Systemf("save the suite state: %v", err))
	}
	eng := suite.Engine(inst)
	doc := suiteInstallDoc{Name: inst.Name, Kind: inst.Kind, Path: inst.Path, Port: inst.Port,
		Priority: inst.Priority, KeepUp: inst.KeepUp, Venv: inst.Venv, Components: componentDocs(inst)}
	return c.Write(engineInstallView{doc: doc, start: eng.Start}, nil)
}

// ---------- show ----------

// engineShowView is `berth engine show`: the suite state, or one install of it.
type engineShowView struct {
	doc  []suiteInstallDoc
	name string
}

func (v engineShowView) Data() any {
	if v.name != "" {
		return v.doc
	}
	return struct {
		Installs []suiteInstallDoc `json:"installs" yaml:"installs"`
	}{v.doc}
}

func (v engineShowView) Human(c *Context, ok bool) error {
	if len(v.doc) == 0 {
		c.Printf("%s No suite installs\n", c.Style.OK(markOK))
		c.Bullet("berth engine install NAME --adopt PATH records a checkout in place")
		return nil
	}
	for _, in := range v.doc {
		line := fmt.Sprintf("%s %s: %s (port %d, %s", c.Style.Bold(in.Name), in.Kind, in.Path, in.Port, in.Priority)
		if in.KeepUp {
			line += ", keep_up"
		}
		if in.Venv != "" {
			line += ", venv " + in.Venv
		}
		line += ")"
		c.Bullet("%s", line)
		for _, cp := range in.Components {
			cline := fmt.Sprintf("%s @ %s", cp.Name, short(cp.Commit))
			if cp.Previous != "" {
				cline += fmt.Sprintf(" (previous %s)", short(cp.Previous))
			}
			if cp.Dirty {
				cline += " [dirty]"
			}
			c.Sub("%s", cline)
		}
	}
	return nil
}

func runEngineShow(c *Context) int {
	fs := c.FlagSet()
	cfgPath := fs.String("config", config.DefaultPath(), "berth.toml ($BERTH_CONFIG); the state file sits beside it")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	name := ""
	if len(c.Args) > 0 {
		name = c.Args[0]
	}
	if len(c.Args) > 1 {
		return c.fail(Systemf("unexpected argument %q", c.Args[1]))
	}
	st, e := loadSuiteState(*cfgPath)
	if e != nil {
		return c.fail(e)
	}
	docs := installDocs(st)
	if name != "" {
		var kept []suiteInstallDoc
		for _, d := range docs {
			if d.Name == name {
				kept = append(kept, d)
			}
		}
		if len(kept) == 0 {
			return c.fail(Systemf("no install named %q in %s", name, suite.SuitePath(*cfgPath)))
		}
		docs = kept
	}
	return c.Write(engineShowView{doc: docs, name: name}, nil)
}

// ---------- update ----------

// engineUpdateDoc is `berth engine update`: what moved, and what was refused.
type engineUpdateDoc struct {
	Install string   `json:"install" yaml:"install"`
	Updated []string `json:"updated" yaml:"updated"`
	Refused []string `json:"refused,omitempty" yaml:"refused,omitempty"`
}

type engineUpdateView struct {
	doc engineUpdateDoc
}

func (v engineUpdateView) Data() any { return v.doc }

func (v engineUpdateView) Human(c *Context, ok bool) error {
	for _, l := range v.doc.Updated {
		c.Bullet("%s", l)
	}
	for _, l := range v.doc.Refused {
		c.Bullet("%s", l)
	}
	return nil
}

// updateInstall moves the install's components to their channels and reports
// each: the moved shas on the ones that moved, the reason on the refused
// ones. A refused component never stops the rest from proceeding.
func updateInstall(m suite.Manager, inst *suite.Install, only, channel string) engineUpdateDoc {
	doc := engineUpdateDoc{Install: inst.Name}
	var idx []int
	for i := range inst.Components {
		if only == "" || inst.Components[i].Name == only {
			idx = append(idx, i)
		}
	}
	if only != "" && len(idx) == 0 {
		doc.Refused = append(doc.Refused, fmt.Sprintf("no component named %s", only))
		return doc
	}
	for _, i := range idx {
		cp := &inst.Components[i]
		dir := inst.ComponentDir(*cp)
		before := cp.Commit
		if err := suite.RefreshDirty(m.Git, dir, cp); err != nil {
			doc.Refused = append(doc.Refused, fmt.Sprintf("%s: re-read the tree: %v", cp.Name, err))
			continue
		}
		if err := suite.Update(m.Git, dir, cp, channel); err != nil {
			doc.Refused = append(doc.Refused, fmt.Sprintf("%s: %v", cp.Name, err))
			continue
		}
		if cp.Commit == before {
			doc.Updated = append(doc.Updated, fmt.Sprintf("%s: %s already at the tip", cp.Name, short(before)))
		} else {
			doc.Updated = append(doc.Updated, fmt.Sprintf("%s: %s -> %s", cp.Name, short(before), short(cp.Commit)))
		}
	}
	return doc
}

func runEngineUpdate(c *Context) int {
	fs := c.FlagSet()
	cfgPath := fs.String("config", config.DefaultPath(), "berth.toml ($BERTH_CONFIG); the state file sits beside it")
	component := fs.String("component", "", "update this component only (default: every component)")
	all := fs.Bool("all", false, "update every component (the default; named for clarity)")
	channel := fs.String("channel", "", "the remote ref to pin to, for a component with no channel of its own")
	var packs stringList
	fs.Var(&packs, "pack", "a node pack to clone in and update (repeatable)")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("berth engine update NAME takes exactly one argument").
			WithHint("usage: berth engine update NAME [--component C | --all] [flags]"))
	}
	if *component != "" && *all {
		return c.fail(Failf("--component and --all are mutually exclusive"))
	}
	name := c.Args[0]
	st, e := loadSuiteState(*cfgPath)
	if e != nil {
		return c.fail(e)
	}
	idx := findInstallIndex(st, name)
	if idx < 0 {
		return c.fail(Failf("no install named %q in %s", name, suite.SuitePath(*cfgPath)))
	}
	inst := &st.Installs[idx]
	// the job guard: a running queue is never cut off, and a queue that
	// cannot be read is not touched either
	n, err := queueRunning(*inst)
	if err != nil {
		return c.fail(Systemf("the queue of %s cannot be read: %v", name, err))
	}
	if n > 0 {
		return c.fail(Failf("%s has %d job%s running; update at the job boundary, when the queue is empty",
			name, n, plural(n)))
	}
	m := engineSuiteDeps(c.Log())
	if len(packs.vals) > 0 {
		if err := m.AddPacks(inst, packs.vals); err != nil {
			return c.fail(Systemf("%v", err))
		}
	}
	doc := updateInstall(m, inst, *component, *channel)
	// the state records what actually happened on disk, refused components
	// included (a freshly dirty tree is now recorded as dirty)
	if err := suite.Save(suite.SuitePath(*cfgPath), st); err != nil {
		return c.fail(Systemf("save the suite state: %v", err))
	}
	if len(doc.Updated) == 0 {
		e := Failf("no component of %s was updated", name)
		for _, r := range doc.Refused {
			e.Detail("%s", r)
		}
		return c.Write(engineUpdateView{doc: doc}, e)
	}
	var failure *Error
	if len(doc.Refused) > 0 {
		failure = Failf("some components of %s were refused", name)
		for _, r := range doc.Refused {
			failure.Detail("%s", r)
		}
	}
	return c.Write(engineUpdateView{doc: doc}, failure)
}

// ---------- rollback ----------

type engineRollbackView struct {
	install   string
	component string
	from      string
	to        string
}

func (v engineRollbackView) Data() any {
	return struct {
		Install   string `json:"install" yaml:"install"`
		Component string `json:"component" yaml:"component"`
		From      string `json:"from" yaml:"from"`
		To        string `json:"to" yaml:"to"`
	}{v.install, v.component, v.from, v.to}
}

func (v engineRollbackView) Human(c *Context, ok bool) error {
	c.Bullet("%s: %s -> %s", v.component, short(v.from), short(v.to))
	return nil
}

func runEngineRollback(c *Context) int {
	fs := c.FlagSet()
	cfgPath := fs.String("config", config.DefaultPath(), "berth.toml ($BERTH_CONFIG); the state file sits beside it")
	component := fs.String("component", "", "the component to roll back (required)")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("berth engine rollback NAME takes exactly one argument").
			WithHint("usage: berth engine rollback NAME --component C [flags]"))
	}
	if *component == "" {
		return c.fail(Failf("--component is required: the component to roll back"))
	}
	name := c.Args[0]
	st, e := loadSuiteState(*cfgPath)
	if e != nil {
		return c.fail(e)
	}
	idx := findInstallIndex(st, name)
	if idx < 0 {
		return c.fail(Failf("no install named %q in %s", name, suite.SuitePath(*cfgPath)))
	}
	inst := &st.Installs[idx]
	n, err := queueRunning(*inst)
	if err != nil {
		return c.fail(Systemf("the queue of %s cannot be read: %v", name, err))
	}
	if n > 0 {
		return c.fail(Failf("%s has %d job%s running; roll back at the job boundary, when the queue is empty",
			name, n, plural(n)))
	}
	cidx := -1
	for i := range inst.Components {
		if inst.Components[i].Name == *component {
			cidx = i
			break
		}
	}
	if cidx < 0 {
		return c.fail(Failf("no component named %q in %s", *component, name))
	}
	m := engineSuiteDeps(c.Log())
	cp := &inst.Components[cidx]
	dir := inst.ComponentDir(*cp)
	if err := suite.RefreshDirty(m.Git, dir, cp); err != nil {
		return c.fail(Systemf("re-read the tree of %s: %v", *component, err))
	}
	from := cp.Commit
	if err := suite.Rollback(m.Git, dir, cp); err != nil {
		return c.fail(Failf("%v", err))
	}
	if err := suite.Save(suite.SuitePath(*cfgPath), st); err != nil {
		return c.fail(Systemf("save the suite state: %v", err))
	}
	return c.Write(engineRollbackView{install: name, component: cp.Name, from: from, to: cp.Commit}, nil)
}

// ---------- remove ----------

type engineRemoveView struct {
	install string
	deleted bool
}

func (v engineRemoveView) Data() any {
	return struct {
		Install string `json:"install" yaml:"install"`
		Deleted bool   `json:"directory_deleted" yaml:"directory_deleted"`
	}{v.install, v.deleted}
}

func (v engineRemoveView) Human(c *Context, ok bool) error {
	if v.deleted {
		c.Bullet("the install directory was removed")
	} else {
		c.Bullet("the directory, its venv and its extra_model_paths.yaml stay where they are")
	}
	return nil
}

func runEngineRemove(c *Context) int {
	fs := c.FlagSet()
	cfgPath := fs.String("config", config.DefaultPath(), "berth.toml ($BERTH_CONFIG); the state file sits beside it")
	del := fs.Bool("delete", false, "also remove the install directory (requires --yes)")
	yes := fs.Bool("yes", false, "consent to --delete removing the directory")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("berth engine remove NAME takes exactly one argument").
			WithHint("usage: berth engine remove NAME [--delete --yes] [flags]"))
	}
	name := c.Args[0]
	if *del && !*yes {
		return c.fail(Failf("--delete removes the directory of %s; pass --yes to consent", name))
	}
	st, e := loadSuiteState(*cfgPath)
	if e != nil {
		return c.fail(e)
	}
	idx := findInstallIndex(st, name)
	if idx < 0 {
		return c.fail(Failf("no install named %q in %s", name, suite.SuitePath(*cfgPath)))
	}
	if err := suite.Remove(&st, name, *del); err != nil {
		return c.fail(Systemf("%v", err))
	}
	if err := suite.Save(suite.SuitePath(*cfgPath), st); err != nil {
		return c.fail(Systemf("save the suite state: %v", err))
	}
	return c.Write(engineRemoveView{install: name, deleted: *del}, nil)
}
