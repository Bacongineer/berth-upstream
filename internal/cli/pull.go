package cli

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/T-Crypt/berth/internal/hf"
)

// defaultModelsDir is where pulled models land: $BERTH_MODELS, else ~/models.
func defaultModelsDir() string {
	if d := os.Getenv("BERTH_MODELS"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "models"
	}
	return filepath.Join(home, "models")
}

type pullDoc struct {
	Repo   string       `json:"repo" yaml:"repo"`
	Commit string       `json:"commit" yaml:"commit"`
	Dir    string       `json:"dir" yaml:"dir"`
	Files  []pulledFile `json:"files" yaml:"files"`
	DryRun bool         `json:"dry_run,omitempty" yaml:"dry_run,omitempty"`
}

type pulledFile struct {
	Path     string `json:"path" yaml:"path"`
	Size     int64  `json:"size" yaml:"size"`
	SHA256   string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	Verified bool   `json:"verified" yaml:"verified"` // size and sha256 matched the Hub's
	Skipped  bool   `json:"skipped,omitempty" yaml:"skipped,omitempty"`
}

type pullView struct{ doc pullDoc }

func (v pullView) Data() any { return v.doc }

func (v pullView) Human(c *Context, ok bool) error {
	verb := "Pulled"
	if v.doc.DryRun {
		verb = "Would pull"
	}
	var total int64
	for _, f := range v.doc.Files {
		total += f.Size
	}
	c.Printf("%s %s %s @ %.12s (%s) into %s\n", c.Style.OK(markOK), verb, v.doc.Repo, v.doc.Commit, gib(total), v.doc.Dir)
	for _, f := range v.doc.Files {
		state := "sha256 verified"
		switch {
		case v.doc.DryRun:
			state = "sha256 " + short(f.SHA256)
		case f.Skipped:
			state = "already here, sha256 verified"
		case f.SHA256 == "":
			state = "size verified (no sha256 published)"
		}
		c.Bullet("%s  %s  %s", f.Path, gib(f.Size), state)
	}
	return nil
}

func gib(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		return "none"
	}
	return s
}

func runPull(c *Context) int {
	fs := c.FlagSet()
	dir := fs.String("dir", defaultModelsDir(), "models directory; files land in DIR/<org>/<repo>/ ($BERTH_MODELS)")
	dry := fs.Bool("dry-run", false, "list what would be pulled and its size; download nothing")
	tokenEnv := fs.String("token-env", "HF_TOKEN", "environment variable holding a Hub token, for gated or private repos")
	if code, done := c.ParseInterspersed(fs); done {
		return code
	}
	if len(c.Args) != 1 {
		return c.fail(Systemf("pull takes one model reference").
			WithHint("berth pull hf:unsloth/Qwen3-30B-A3B-GGUF:Q4_K_M"))
	}
	ref, err := hf.ParseRef(c.Args[0])
	if err != nil {
		return c.fail(Failf("%v", err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cl := &hf.Client{Token: os.Getenv(*tokenEnv)}

	commit, err := cl.Commit(ctx, ref.Repo, ref.Revision)
	if err != nil {
		return c.fail(Systemf("%v", err))
	}
	tree, err := cl.Tree(ctx, ref.Repo, commit)
	if err != nil {
		return c.fail(Systemf("%v", err))
	}
	files, err := hf.Select(tree, ref.Selector)
	if err != nil {
		return c.fail(Failf("%v", err).WithHint("name a quant (Q4_K_M), a file, or a glob after the repo: hf:org/repo:Q4_K_M"))
	}
	if miss := hf.Missing(files); len(miss) > 0 {
		for _, p := range miss {
			for _, f := range tree {
				if f.Path == p {
					files = append(files, f)
				}
			}
		}
	}
	dest := filepath.Join(*dir, filepath.FromSlash(ref.Repo))
	doc := pullDoc{Repo: ref.Repo, Commit: commit, Dir: dest, DryRun: *dry}
	for _, f := range files {
		doc.Files = append(doc.Files, pulledFile{Path: f.Path, Size: f.Size, SHA256: f.SHA256})
	}
	if *dry {
		return c.Write(pullView{doc: doc}, nil)
	}

	start := time.Now()
	live := !c.Machine() && isTerminal(c.Err)
	for i, f := range files {
		t0 := time.Now()
		skipped, err := cl.Download(ctx, ref.Repo, commit, f, dest, func(name string, done, total int64) {
			if !live {
				return
			}
			rate := float64(done) / time.Since(t0).Seconds() / (1 << 20)
			fmt.Fprintf(c.Err, "\rberth: %s  %5.1f%%  %s of %s  %.0f MiB/s   ", name, 100*float64(done)/float64(max(total, 1)), gib(done), gib(total), rate)
		})
		if live {
			fmt.Fprintln(c.Err)
		} else if !c.Machine() && err == nil {
			fmt.Fprintf(c.Err, "berth: %s %s (%s)\n", f.Path, map[bool]string{true: "already here", false: "downloaded and verified"}[skipped], gib(f.Size))
		}
		if err != nil {
			if ctx.Err() != nil {
				return c.fail(Systemf("pull interrupted; run the same command again to resume %s", f.Path))
			}
			return c.fail(Failf("%s: %v", f.Path, err))
		}
		doc.Files[i].Verified, doc.Files[i].Skipped = true, skipped
	}
	if err := hf.WriteManifest(dest, hf.Manifest{Repo: ref.Repo, Revision: ref.Revision, Commit: commit, Files: files, Pulled: time.Now().UTC()}); err != nil {
		return c.fail(Systemf("write the manifest: %v", err))
	}
	if !c.Machine() {
		fmt.Fprintf(c.Err, "berth: done in %s\n", time.Since(start).Round(time.Second))
	}
	return c.Write(pullView{doc: doc}, nil)
}

type verifyDoc struct {
	Dirs []verifyDir `json:"dirs" yaml:"dirs"`
	Bad  int         `json:"bad" yaml:"bad"`
}

type verifyDir struct {
	Dir    string     `json:"dir" yaml:"dir"`
	Repo   string     `json:"repo,omitempty" yaml:"repo,omitempty"`
	Checks []hf.Check `json:"checks" yaml:"checks"`
	Error  string     `json:"error,omitempty" yaml:"error,omitempty"`
}

type verifyView struct{ doc verifyDoc }

func (v verifyView) Data() any { return v.doc }

func (v verifyView) Human(c *Context, ok bool) error {
	if len(v.doc.Dirs) == 0 {
		c.Printf("%s Nothing to verify: no %s under the models directory\n", c.Style.OK(markOK), hf.ManifestName)
		return nil
	}
	for _, d := range v.doc.Dirs {
		if d.Error != "" {
			c.Printf("%s %s: %s\n", c.Style.Err(markError), d.Dir, d.Error)
			continue
		}
		c.Printf("%s\n", d.Repo+"  "+c.Style.Dim(d.Dir))
		for _, ch := range d.Checks {
			mark := c.Style.OK(markOK)
			if ch.Status != "ok" && ch.Status != "unhashed" {
				mark = c.Style.Err(markError)
			}
			line := fmt.Sprintf("%s %s", mark, ch.Path)
			switch {
			case ch.Detail != "":
				line += "  " + ch.Status + ": " + ch.Detail
			case ch.Status != "ok":
				line += "  " + ch.Status
			}
			c.Bullet("%s", line)
		}
	}
	if v.doc.Bad == 0 {
		c.Printf("%s every file matches the Hub's size and sha256\n", c.Style.OK(markOK))
	}
	return nil
}

func runVerify(c *Context) int {
	fset := c.FlagSet()
	dir := fset.String("dir", defaultModelsDir(), "models directory searched when no DIR is given ($BERTH_MODELS)")
	if code, done := c.ParseInterspersed(fset); done {
		return code
	}
	dirs := c.Args
	if len(dirs) == 0 {
		_ = filepath.WalkDir(*dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && strings.Count(strings.TrimPrefix(p, *dir), string(filepath.Separator)) > 3 {
				return filepath.SkipDir
			}
			if d.Name() == hf.ManifestName {
				dirs = append(dirs, filepath.Dir(p))
			}
			return nil
		})
	}
	var doc verifyDoc
	live := !c.Machine() && isTerminal(c.Err)
	for _, d := range dirs {
		vd := verifyDir{Dir: d}
		if m, err := hf.ReadManifest(d); err == nil {
			vd.Repo = m.Repo
		}
		checks, err := hf.Verify(d, func(file string, done, total int64) {
			if live {
				fmt.Fprintf(c.Err, "\rberth: hashing %s  %5.1f%%   ", file, 100*float64(done)/float64(max(total, 1)))
			}
		})
		if live {
			fmt.Fprint(c.Err, "\r\033[K")
		}
		if err != nil {
			vd.Error = err.Error()
			doc.Bad++
		}
		for _, ch := range checks {
			if ch.Status != "ok" && ch.Status != "unhashed" {
				doc.Bad++
			}
		}
		vd.Checks = checks
		doc.Dirs = append(doc.Dirs, vd)
	}
	if doc.Bad > 0 {
		return c.Write(verifyView{doc: doc}, Failf("%d file(s) failed verification", doc.Bad).
			WithHint("berth pull the same reference again: it re-downloads only what does not match"))
	}
	return c.Write(verifyView{doc: doc}, nil)
}
