// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"os"
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
	c.Bullet("Agents load them on their next session: berth tells them to ask Berth for the card instead of killing GPU processes, comfyui how to generate through Berth")
	return nil
}

func runSkillInstall(c *Context) int {
	fs := c.FlagSet()
	target := fs.String("target", "all", "claude, agents, hermes, opencode or all (comma-separated; all includes hermes when its home exists)")
	dir := fs.String("dir", "", "install into this skills directory instead (DIR/berth/SKILL.md, DIR/comfyui/SKILL.md)")
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
			names = skill.All(home)
		}
		for _, n := range names {
			n = strings.TrimSpace(n)
			dir, ok := skill.Dir(n, home)
			if !ok {
				return c.fail(Systemf("unknown target %q", n).WithHint("targets: %s, all; or --dir", strings.Join(skill.Targets, ", ")))
			}
			places = append(places, skillPlace{Target: n, Path: dir})
		}
	}
	var doc skillInstallDoc
	for _, p := range places {
		for _, s := range skill.Skills {
			path, changed, err := skill.Install(p.Path, s)
			if err != nil {
				return c.fail(Systemf("install the %s skill for %s: %v", s.Name, p.Target, err))
			}
			doc.Installed = append(doc.Installed, skillPlace{Target: p.Target, Path: path, Changed: changed})
		}
	}
	return c.Write(skillInstallView{doc: doc}, nil)
}

func runSkillShow(c *Context) int {
	fs := c.FlagSet()
	if code, done := c.Parse(fs); done {
		return code
	}
	name := "berth"
	switch args := fs.Args(); len(args) {
	case 0:
	case 1:
		name = args[0]
	default:
		return c.fail(Failf("skill show takes one skill name"))
	}
	s, ok := skill.Find(name)
	if !ok {
		return c.fail(Failf("no skill named %q", name).WithHint("skills: berth, comfyui"))
	}
	_, _ = c.Out.Write(s.Doc)
	return 0
}
