package suite

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Shell is the seam over the non-git steps of an install: venv, pip and
// clone. A test records the calls instead of running them.
type Shell interface {
	Run(bin string, dir string, args ...string) error
}

// Manager does the install work: git through the Git seam, the rest through
// Shell. Log may be nil for a one-off use that wants no output.
type Manager struct {
	Git   Git
	Shell Shell
	Log   *slog.Logger
}

// NewManager returns a Manager over the system git binary and shell.
func NewManager(log *slog.Logger) Manager {
	return Manager{Git: NewGit(), Shell: realShell{}, Log: log}
}

// realShell runs the step and, on failure, names the step and its output.
type realShell struct{}

func (realShell) Run(bin string, dir string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return nil
}

// ErrNotACheckout names the refusal to adopt a path that is not a ComfyUI
// checkout (no such directory, no main.py or no .git); the CLI maps it to a
// validation refusal, while a git read failure behind a valid checkout is a
// system failure.
var ErrNotACheckout = errors.New("not a ComfyUI checkout")

// isCheckout reports whether dir is a git checkout: the marker is a .git
// entry, a directory on a normal clone and a file on a worktree or submodule.
func isCheckout(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (fi.IsDir() || fi.Mode().IsRegular())
}

// detectVenv names the venv a checkout already has: .venv first, then venv.
func detectVenv(path string) string {
	for _, v := range []string{".venv", "venv"} {
		if fi, err := os.Stat(filepath.Join(path, v)); err == nil && fi.IsDir() {
			return v
		}
	}
	return ""
}

// venvPython is the python inside the install's venv: bin/ on POSIX,
// Scripts/ on Windows.
func venvPython(installPath, venv string) string {
	base := filepath.Join(installPath, venv)
	if runtime.GOOS == "windows" {
		return filepath.Join(base, "Scripts", "python.exe")
	}
	return filepath.Join(base, "bin", "python")
}

// componentAt reads one checkout's git state as a component: the origin
// remote ("" when there is none, which marks the component rollback-only),
// the pinned sha, the branch it sits on, and whether the tree is dirty.
func (m Manager) componentAt(dir, name, relPath string) (Component, error) {
	cp := Component{Name: name, Path: relPath}
	repo, err := m.Git.RemoteURL(dir)
	if err != nil {
		return cp, err
	}
	cp.Repo = repo
	head, err := m.Git.Head(dir)
	if err != nil {
		return cp, err
	}
	cp.Commit = head
	branch, err := m.Git.Branch(dir)
	if err != nil {
		return cp, err
	}
	cp.Channel = branch
	dirty, err := m.Git.Dirty(dir)
	if err != nil {
		return cp, err
	}
	cp.Dirty = dirty
	return cp, nil
}

// investigate surveys a checkout: the core component at the root, then every
// directory under custom_nodes that is itself a git checkout (the core's
// ComfyUI-Manager and the git-based node packs). A non-git directory is not
// recorded and is never touched.
func (m Manager) investigate(path string) ([]Component, error) {
	core, err := m.componentAt(path, KindComfyUI, "")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	components := []Component{core}
	packs, err := os.ReadDir(filepath.Join(path, "custom_nodes"))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return components, nil
	}
	for _, e := range packs {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(path, "custom_nodes", name)
		if !isCheckout(dir) {
			continue
		}
		cp, err := m.componentAt(dir, name, "custom_nodes/"+name)
		if err != nil {
			return nil, err
		}
		components = append(components, cp)
	}
	return components, nil
}

// Adopt registers an existing ComfyUI checkout in place: it reads the git
// state (it never changes it) and records the core, the venv, ComfyUI-Manager
// and every git-based node pack.
func (m Manager) Adopt(name, path string, port int, priority string, keepUp bool) (Install, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return Install{}, errors.Join(ErrNotACheckout, fmt.Errorf("%s: no such directory", path))
	} else if err != nil {
		return Install{}, err
	} else if !fi.IsDir() {
		return Install{}, errors.Join(ErrNotACheckout, fmt.Errorf("%s is not a directory", path))
	}
	var missing []string
	if _, err := os.Stat(filepath.Join(path, "main.py")); errors.Is(err, os.ErrNotExist) {
		missing = append(missing, "main.py")
	}
	if !isCheckout(path) {
		missing = append(missing, ".git (a git checkout)")
	}
	if len(missing) > 0 {
		return Install{}, errors.Join(ErrNotACheckout,
			fmt.Errorf("%s: not a ComfyUI checkout, missing %s", path, strings.Join(missing, " and ")))
	}
	components, err := m.investigate(path)
	if err != nil {
		return Install{}, err
	}
	if m.Log != nil {
		m.Log.Info("adopted", "install", name, "path", path, "port", port, "components", len(components))
	}
	return Install{Name: name, Kind: KindComfyUI, Path: path, Port: port, Priority: priority,
		KeepUp: keepUp, Venv: detectVenv(path), Components: components}, nil
}

// InstallOpts is a fresh install's parameters; the zero values take the
// defaults: the ComfyUI repo on master under $XDG_DATA_HOME.
type InstallOpts struct {
	Path     string
	Repo     string
	Channel  string
	Port     int
	Priority string
	KeepUp   bool
	Packs    []string
}

// DefaultInstallPath is where a fresh install lands:
// $XDG_DATA_HOME/berth/comfyui/<name>, or ~/.local/share/berth/comfyui/<name>
// when the variable is not set.
func DefaultInstallPath(name string) string {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, ".local", "share")
		}
	}
	if base == "" {
		return filepath.Join("berth", "comfyui", name)
	}
	return filepath.Join(base, "berth", "comfyui", name)
}

// Install creates a fresh checkout: git clone (on the channel's branch), the
// .venv, the requirements through the venv's python, the requested packs, and
// the same component survey as Adopt. PyTorch is left to the operator; the
// install is registered either way, and ComfyUI's own start error names the
// missing piece.
func (m Manager) Install(name string, opts InstallOpts) (Install, error) {
	path := opts.Path
	if path == "" {
		path = DefaultInstallPath(name)
	}
	repo := opts.Repo
	if repo == "" {
		repo = "https://github.com/comfyanonymous/ComfyUI.git"
	}
	channel := opts.Channel
	if channel == "" {
		channel = "master"
	}
	if m.Log != nil {
		m.Log.Info("installing", "install", name, "repo", repo, "channel", channel, "path", path)
	}
	clone := []string{"clone"}
	if channel != "" {
		clone = append(clone, "--branch", channel)
	}
	if err := m.Shell.Run("git", "", append(clone, repo, path)...); err != nil {
		return Install{}, fmt.Errorf("clone %s: %w", repo, err)
	}
	if err := m.Shell.Run("python3", path, "-m", "venv", ".venv"); err != nil {
		return Install{}, fmt.Errorf("venv in %s: %w", path, err)
	}
	py := venvPython(path, ".venv")
	if err := m.Shell.Run(py, path, "-m", "pip", "install", "-r", "requirements.txt"); err != nil {
		return Install{}, fmt.Errorf("requirements in %s: %w", path, err)
	}
	inst := Install{Name: name, Kind: KindComfyUI, Path: path, Port: opts.Port, Priority: opts.Priority,
		KeepUp: opts.KeepUp, Venv: ".venv"}
	if len(opts.Packs) > 0 {
		if err := m.AddPacks(&inst, opts.Packs); err != nil {
			return Install{}, err
		}
	}
	components, err := m.investigate(path)
	if err != nil {
		return Install{}, err
	}
	inst.Components = components
	return inst, nil
}

// AddPacks clones each pack URL into custom_nodes/<last path segment>, skips
// one already present by name, syncs the pack's own requirements.txt when it
// has one, and records the component.
func (m Manager) AddPacks(inst *Install, urls []string) error {
	py := "python3"
	if inst.Venv != "" {
		py = venvPython(inst.Path, inst.Venv)
	}
	for _, u := range urls {
		name := lastSegment(u)
		if name == "" {
			return fmt.Errorf("pack %q: cannot tell its name from the URL", u)
		}
		dir := filepath.Join(inst.Path, "custom_nodes", name)
		if isCheckout(dir) {
			continue
		}
		if err := m.Shell.Run("git", "", "clone", u, dir); err != nil {
			return fmt.Errorf("clone pack %s: %w", u, err)
		}
		if fi, err := os.Stat(filepath.Join(dir, "requirements.txt")); err == nil && fi.Mode().IsRegular() {
			if err := m.Shell.Run(py, dir, "-m", "pip", "install", "-r", "requirements.txt"); err != nil {
				return fmt.Errorf("pack %s requirements: %w", name, err)
			}
		}
		cp, err := m.componentAt(dir, name, "custom_nodes/"+name)
		if err != nil {
			return err
		}
		inst.Components = append(inst.Components, cp)
	}
	return nil
}

// lastSegment is the repo name a pack URL points at: the last path segment,
// without a .git suffix.
func lastSegment(u string) string {
	s := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git"), "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		s = s[i+1:] // a git@host:org/repo style URL
	}
	return s
}

// Remove drops the install from the state; with deleteDir, the install's
// directory goes with it (venv and extra_model_paths.yaml included). The
// flag pair --delete --yes is the consent for that one irreversible step.
func Remove(st *State, name string, deleteDir bool) error {
	idx := -1
	for i, in := range st.Installs {
		if in.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no install named %q in the suite state", name)
	}
	in := st.Installs[idx]
	if deleteDir {
		if err := os.RemoveAll(in.Path); err != nil {
			return fmt.Errorf("remove %s: %w", in.Path, err)
		}
	}
	st.Installs = append(st.Installs[:idx], st.Installs[idx+1:]...)
	return nil
}
