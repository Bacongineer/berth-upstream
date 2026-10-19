package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/T-Crypt/berth/internal/models"
)

// `berth models export ollama NAME` writes the Modelfile that hands a GGUF this
// machine already has to Ollama, and runs the create. Berth never copies,
// converts or deletes a weight file: FROM names the file where it is, and it is
// Ollama that decides what a model file is called and where its blob lives.

// ollamaMaxCtx is the window an export asks for: a model's own window when it
// has a small one, and Ollama's default ceiling when it has a bigger one. The
// real limit of an export is fitting the KV cache beside the weights, so asking
// for a 262144 window is not a promise.
const ollamaMaxCtx = 32768

// ollamaExportDoc is what `--format json` reports: the name, the Modelfile, the
// command and what the command said.
type ollamaExportDoc struct {
	Status    string   `json:"status" yaml:"status"`       // ok | failed
	Model     string   `json:"model" yaml:"model"`         // the Ollama name it would create
	Path      string   `json:"path" yaml:"path"`           // the source GGUF
	Modelfile string   `json:"modelfile" yaml:"modelfile"` // where it was written
	Body      string   `json:"body" yaml:"body"`           // what it says
	Companion string   `json:"companion,omitempty" yaml:"companion,omitempty"`
	Command   []string `json:"command" yaml:"command"`
	Ran       bool     `json:"ran" yaml:"ran"`
	Err       string   `json:"err,omitempty" yaml:"err,omitempty"`
}

type ollamaExportView struct{ doc ollamaExportDoc }

func (v ollamaExportView) Data() any { return v.doc }

func (v ollamaExportView) Human(c *Context, ok bool) error {
	d := v.doc
	if ok {
		c.Printf("%s %s\n", c.Style.OK(markOK), c.Style.Bold(d.Model))
	} else {
		c.Printf("%s %s\n", c.Style.Err(markError), c.Style.Bold(d.Model))
	}
	c.Sub("from: %s", d.Path)
	if d.Companion != "" {
		c.Sub("with: %s", d.Companion)
	}
	c.Sub("Modelfile (%s):", d.Modelfile)
	for _, line := range strings.Split(strings.TrimRight(d.Body, "\n"), "\n") {
		c.Printf("%s%s\n", subIndent, line)
	}
	c.Sub("command: %s", strings.Join(d.Command, " "))
	if !d.Ran {
		c.Sub("nothing was run")
	}
	if d.Err != "" {
		c.Sub("%s", c.Style.Err(d.Err))
	}
	return nil
}

func runModelsExport(c *Context) int {
	var dirs stringList
	fs := c.FlagSet()
	fs.Var(&dirs, "dir", "where to look for a name, repeatable (default: berth.toml's [models] dirs)")
	as := fs.String("as", "", "the Ollama name to create (default: the file's stem, with the quant as its tag)")
	dryRun := fs.Bool("dry-run", false, "write the Modelfile and print the command, run nothing")
	// The two positionals may sit anywhere among the flags: the flag package
	// stops at the first bare word, so parsing resumes after each one.
	pos := []string{}
	for {
		if code, done := c.Parse(fs); done {
			return code
		}
		if len(c.Args) == 0 {
			break
		}
		pos = append(pos, c.Args[0])
		c.Args = c.Args[1:]
		if len(pos) > 2 {
			return c.fail(Failf("unexpected argument %q", pos[2]))
		}
	}
	if len(pos) == 0 {
		return c.fail(Failf("models export needs an engine and a model").
			WithHint("run \"berth models export ollama NAME\" (add --dry-run to see the Modelfile first)"))
	}
	kind := pos[0]
	if kind != "ollama" {
		return c.fail(Failf("no export for %q", kind).WithHint("the export of a model file is ollama"))
	}
	if len(pos) < 2 {
		return c.fail(Failf("models export ollama needs a model name or a path").
			WithHint("run \"berth models\" for the list"))
	}
	arg := pos[1]

	m, _, err := showModel(arg, modelRoots(c, dirs.vals))
	if err != nil {
		return c.fail(Failf("%s: %v", arg, err))
	}
	if m.Format != models.FormatGGUF {
		return c.fail(Failf("%s is %s: Ollama is given a GGUF", m.Name, m.Format).
			WithHint("export the safetensors directory with ollama's own importer instead"))
	}
	if m.Parts > 1 {
		return c.fail(Failf("%s is split into %d files and Ollama reads one file", m.Name, m.Parts).
			WithHint("merge it first: llama-gguf-split --merge <part1> <merged>"))
	}
	abs, err := filepath.Abs(m.Path)
	if err != nil {
		return c.fail(Systemf("%s: %v", m.Path, err))
	}
	name := *as
	if name == "" {
		name = ollamaName(models.Stem(abs), m.Quant)
	}
	companion := firstCompanion(m)
	body := modelfile(abs, companion, m.Context)

	dir, err := os.MkdirTemp("", "berth-export-")
	if err != nil {
		return c.fail(Systemf("cannot write a Modelfile: %v", err))
	}
	// A Modelfile that Ollama has already read is litter; one the operator was
	// told to run themselves, or that a failing create stopped on, is the only
	// copy of what was asked for. keep says which.
	keep := true
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "Modelfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return c.fail(Systemf("cannot write %s: %v", path, err))
	}

	cmd := []string{"ollama", "create", name, "-f", path}
	doc := ollamaExportDoc{Status: "ok", Model: name, Path: abs, Modelfile: path, Body: body,
		Companion: companion, Command: cmd}
	if *dryRun {
		return c.Write(ollamaExportView{doc: doc}, nil)
	}
	bin, err := exec.LookPath("ollama")
	if err != nil {
		doc.Status, doc.Err = "failed", "ollama is not installed on this machine"
		return c.Write(ollamaExportView{doc: doc}, Failf("ollama is not installed: cannot create %s", name).
			WithHint("install Ollama, or run the command above once it is: the Modelfile is still at %s", path))
	}
	doc.Command[0] = bin
	run := exec.Command(bin, cmd[1:]...)
	run.Stdout = c.Out
	run.Stderr = c.Err
	doc.Ran = true
	if err := run.Run(); err != nil {
		doc.Status, doc.Err = "failed", err.Error()
		return c.Write(ollamaExportView{doc: doc}, Failf("ollama create %s: %v", name, err).
			WithHint("Ollama copies the GGUF into its own store: check its disk before retrying a failure "+
				"(the Modelfile is kept at %s)", path))
	}
	keep = false // Ollama has read it; nothing of Berth's is left behind
	return c.Write(ollamaExportView{doc: doc}, nil)
}

// modelfile is the whole file Berth writes: the model, its vision projector when
// it has one, and a window. No TEMPLATE: Ollama reads a GGUF's own chat template
// and its own tokenizer, and rewriting a ChatML template into Modelfile syntax
// is where an export goes quietly wrong on the tool calls it no longer emits.
func modelfile(gguf, companion string, ctx uint64) string {
	var b strings.Builder
	b.WriteString("# written by berth models export ollama\n")
	fmt.Fprintf(&b, "FROM %s\n", gguf)
	if companion != "" {
		fmt.Fprintf(&b, "FROM %s\n", companion) // the vision projector, by Ollama's second-FROM rule
	}
	if ctx > 0 {
		if ctx > ollamaMaxCtx {
			ctx = ollamaMaxCtx
		}
		fmt.Fprintf(&b, "PARAMETER num_ctx %d\n", ctx)
	}
	return b.String()
}

// firstCompanion is the model's vision projector, if it has one: the second FROM
// line Ollama wants. A draft companion is not exported - Ollama has no slot for
// an external speculative draft.
func firstCompanion(m models.Model) string {
	for _, p := range m.Companions {
		if models.RoleOf("", p) == models.RoleMMProj {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}

// ollamaName is the name a file gets when nobody names it: its stem, lowercased,
// with its quant as the tag. Ollama's own character set is narrow, so anything
// else a filename carries becomes a dash.
func ollamaName(stem, quant string) string {
	name := sanitize(stem)
	if name == "" {
		name = "model"
	}
	tag := sanitize(quant)
	if tag == "" {
		tag = "latest"
	}
	return name + ":" + tag
}

// sanitize is Ollama's model-name grammar: lower case letters, digits, and
// . _ / - : with everything else folded to a dash.
func sanitize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		keep := r == '.' || r == '_' || r == '/' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case keep:
			b.WriteRune(r)
			dash = false
		case r == ' ' || r == '-' || r == ':':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
