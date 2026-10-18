package cli

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/T-Crypt/berth/internal/skill"
)

// skillInstallDoc is what `berth skill install --format json|yaml` prints.
type skillInstallDoc struct {
	Installed []skillPlace `json:"installed" yaml:"installed"`
}

type skillPlace struct {
	Target  string `json:"target" yaml:"target"`
	Path    string `json:"path" yaml:"path"`
	Changed bool   `json:"changed" yaml:"changed"`
}

type skillInstallView struct{ doc skillInstallDoc }

func (v skillInstallView) Data() any { return v.doc }

func (v skillInstallView) Human(c *Context, ok bool) error {
	for _, p := range v.doc.Installed {
		word := "up to date"
		if p.Changed {
			word = "written"
		}
		c.Printf("%s %s: %s (%s)\n", c.Style.OK(markOK), p.Target, p.Path, word)
	}
	c.Bullet("Agents load it on their next session; it tells them to ask Berth for the card instead of killing GPU processes")
	return nil
}

func runSkillInstall(c *Context) int {
	fs := c.FlagSet()
	target := fs.String("target", "all", "claude, agents, opencode or all (comma-separated)")
	dir := fs.String("dir", "", "install into this skills directory instead (DIR/berth/SKILL.md)")
	if code, done := c.Parse(fs); done {
		return code
	}
	if code, done := c.noArgs(); done {
		return code
	}
	var places []skillPlace
	if *dir != "" {
		places = append(places, skillPlace{Target: "dir", Path: *dir})
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return c.fail(Systemf("no home directory: %v", err))
		}
		names := strings.Split(*target, ",")
		if *target == "all" {
			names = names[:0]
			for n := range skill.Targets {
				names = append(names, n)
			}
			sort.Strings(names)
		}
		for _, n := range names {
			rel, ok := skill.Targets[strings.TrimSpace(n)]
			if !ok {
				return c.fail(Systemf("unknown target %q", n).WithHint("targets: claude, agents, opencode, all; or --dir"))
			}
			places = append(places, skillPlace{Target: strings.TrimSpace(n), Path: filepath.Join(home, rel)})
		}
	}
	var doc skillInstallDoc
	for _, p := range places {
		path, changed, err := skill.Install(p.Path)
		if err != nil {
			return c.fail(Systemf("install the skill for %s: %v", p.Target, err))
		}
		doc.Installed = append(doc.Installed, skillPlace{Target: p.Target, Path: path, Changed: changed})
	}
	return c.Write(skillInstallView{doc: doc}, nil)
}

func runSkillShow(c *Context) int {
	fs := c.FlagSet()
	if code, done := c.Parse(fs); done {
		return code
	}
	if code, done := c.noArgs(); done {
		return code
	}
	_, _ = c.Out.Write(skill.Doc)
	return 0
}
