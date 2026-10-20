// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/models"
	"github.com/T-Crypt/berth/internal/pagecache"
)

// prefetchDoc is what `berth prefetch --format json|yaml` prints: what the
// argument resolved to, the files the kernel was asked to read, and the
// bytes that asks for.
type prefetchDoc struct {
	Status string   `json:"status" yaml:"status"`                     // ok | failed
	Model  string   `json:"model" yaml:"model"`                       // the name or path as given
	Target string   `json:"target,omitempty" yaml:"target,omitempty"` // the model it resolved to
	Files  []string `json:"files,omitempty" yaml:"files,omitempty"`
	Bytes  uint64   `json:"bytes,omitempty" yaml:"bytes,omitempty"` // the ask, in bytes
}

// prefetchView is the document and the per-file sizes the human report shows
// under it; a machine reads the document alone.
type prefetchView struct {
	doc   prefetchDoc
	sizes []uint64
}

func (v prefetchView) Data() any { return v.doc }

func (v prefetchView) Human(c *Context, ok bool) error {
	d := v.doc
	if !ok {
		return nil // the error says what went wrong
	}
	c.Printf("%s asked the kernel to read %d file%s of %s into the page cache\n",
		c.Style.OK(markOK), len(d.Files), plural(len(d.Files)), c.Style.Bold(d.Target))
	for i, f := range d.Files {
		if i < len(v.sizes) {
			c.Sub("%s %s", f, size(v.sizes[i]))
		} else {
			c.Sub("%s", f)
		}
	}
	c.Bullet("the reads run in the background on the kernel's schedule, and it takes the pages back when the machine needs the RAM")
	return nil
}

func runPrefetch(c *Context) int {
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
		return c.fail(Systemf("prefetch needs a model name or a path").
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
	m, _, err := showModel(arg, roots)
	if err != nil {
		if m.Name == "" {
			m.Name, m.Path = filepath.Base(arg), arg
		}
		return c.Write(prefetchView{doc: prefetchDoc{Status: "failed", Model: arg, Target: m.Name}},
			Failf("%s: %v", arg, err))
	}
	files := modelFiles(m)
	view := prefetchView{doc: prefetchDoc{Status: "ok", Model: arg, Target: m.Name, Files: files}}
	for _, f := range files {
		if err := pagecache.Prefetch(f); err != nil {
			view.doc.Status = "failed"
			if errors.Is(err, pagecache.ErrUnsupported) {
				return c.Write(view, Systemf("this system has no page-cache interface, so %s was not asked for", f))
			}
			return c.Write(view, Systemf("%s: %v", f, err))
		}
		if st, err := os.Stat(f); err == nil {
			view.sizes = append(view.sizes, uint64(st.Size()))
			view.doc.Bytes += uint64(st.Size())
		}
	}
	return c.Write(view, nil)
}

// modelFiles is everything a model loads: its files, split parts in order,
// and the vision projector and draft that go with it.
func modelFiles(m models.Model) []string {
	files := make([]string, 0, len(m.Files)+len(m.Companions))
	files = append(files, m.Files...)
	files = append(files, m.Companions...)
	if len(files) == 0 {
		files = []string{m.Path}
	}
	return files
}
