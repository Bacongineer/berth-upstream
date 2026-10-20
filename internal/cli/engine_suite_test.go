package cli

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/suite"
)

// fakeSuiteGit is the suite.Git the cli tests inject: per-dir shas, branches,
// dirty flags, origin URLs and refs, with the checkouts recorded.
type fakeSuiteGit struct {
	heads     map[string]string
	branches  map[string]string
	dirty     map[string]bool
	remotes   map[string]string
	refs      map[string]map[string]string
	checkouts []string
}

func newFakeSuiteGit() *fakeSuiteGit {
	return &fakeSuiteGit{
		heads:    map[string]string{},
		branches: map[string]string{},
		dirty:    map[string]bool{},
		remotes:  map[string]string{},
		refs:     map[string]map[string]string{},
	}
}

func (f *fakeSuiteGit) set(dir, sha, branch, remote string, refs map[string]string) {
	f.heads[dir] = sha
	f.branches[dir] = branch
	if remote != "" {
		f.remotes[dir] = remote
	}
	f.refs[dir] = refs
}

func (f *fakeSuiteGit) RemoteURL(dir string) (string, error) {
	url, ok := f.remotes[dir]
	if !ok {
		return "", nil
	}
	return url, nil
}

func (f *fakeSuiteGit) Head(dir string) (string, error) {
	sha, ok := f.heads[dir]
	if !ok {
		return "", fmt.Errorf("%s: not a checkout", dir)
	}
	return sha, nil
}

func (f *fakeSuiteGit) Branch(dir string) (string, error) { return f.branches[dir], nil }

func (f *fakeSuiteGit) Dirty(dir string) (bool, error) { return f.dirty[dir], nil }

func (f *fakeSuiteGit) Fetch(dir string) error {
	if _, ok := f.heads[dir]; !ok {
		return fmt.Errorf("%s: not a checkout", dir)
	}
	return nil
}

func (f *fakeSuiteGit) Ref(dir, ref string) (string, error) {
	sha, ok := f.refs[dir][ref]
	if !ok {
		return "", fmt.Errorf("%s: unknown ref %s", dir, ref)
	}
	return sha, nil
}

func (f *fakeSuiteGit) Checkout(dir, sha string) error {
	f.heads[dir] = sha
	f.checkouts = append(f.checkouts, dir+" "+sha)
	return nil
}

// fakeSuiteShell is the suite.Shell the cli tests inject: it records the
// steps, and a git clone materializes its target as a checkout.
type fakeSuiteShell struct {
	t     *testing.T
	calls []string
	fail  string
}

func (f *fakeSuiteShell) Run(bin string, dir string, args ...string) error {
	joined := bin + " " + strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	if f.fail != "" && strings.Contains(joined, f.fail) {
		return fmt.Errorf("simulated failure in %q", joined)
	}
	if bin == "git" && len(args) >= 3 && args[0] == "clone" {
		suiteTestTree(f.t, args[len(args)-1])
	}
	return nil
}

// suiteTestTree is a stand-in ComfyUI checkout: main.py, the .git marker, a
// venv, and custom_nodes with one git pack and one plain directory.
func suiteTestTree(t *testing.T, dir string) {
	t.Helper()
	write := func(rel string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.py")
	write(".git/HEAD")
	write(".venv/pyvenv.cfg")
	write("requirements.txt")
	write("custom_nodes/ComfyUI-Manager/.git/HEAD")
	write("custom_nodes/ComfyUI-Manager/main.py")
	write("custom_nodes/a-plain-pack/nodes.py")
}

const (
	suiteShaA = "aaaabbbbccccdddd"
	suiteShaB = "1111222233334444"
	suiteShaC = "5555666677778888"
	suiteShaD = "9999aaaabbbbcccc"
)

var defaultSuiteDeps = engineSuiteDeps

// useSuiteFakes points the suite seam at the fakes for the duration of the
// test, as fakeEngine points the engine readers at a test server.
func useSuiteFakes(t *testing.T, g *fakeSuiteGit, sh *fakeSuiteShell) {
	t.Helper()
	t.Cleanup(func() { engineSuiteDeps = defaultSuiteDeps })
	engineSuiteDeps = func(log *slog.Logger) suite.Manager {
		return suite.Manager{Git: g, Shell: sh, Log: log}
	}
}

// suiteTestConfig writes an isolated berth.toml (discovery off, so the
// machine's real ports are not probed) and returns its path; the suite state
// file sits beside it.
func suiteTestConfig(t *testing.T) string {
	t.Helper()
	isolate(t)
	dir := filepath.Join(t.TempDir(), "config", "berth")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "berth.toml")
	if err := os.WriteFile(path, []byte("[engines]\ndiscover = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// freePort is a port nothing listens on: the queue guard sees a down install.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// stateFile writes the suite state file beside the config.
func stateFile(cfgPath string) string { return suite.SuitePath(cfgPath) }

func writeStateFile(t *testing.T, cfgPath string, st suite.State) {
	t.Helper()
	if err := suite.Save(stateFile(cfgPath), st); err != nil {
		t.Fatal(err)
	}
}

func loadStateFile(t *testing.T, cfgPath string) suite.State {
	t.Helper()
	st, err := suite.Load(stateFile(cfgPath))
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	return st
}

func suiteTestInstall(name string, path string, port int) suite.Install {
	return suite.Install{
		Name: name, Kind: "comfyui", Path: path, Port: port, Priority: "batch", Venv: ".venv",
		Components: []suite.Component{
			{Name: "comfyui", Repo: "https://github.com/comfyanonymous/ComfyUI.git",
				Channel: "master", Commit: suiteShaA},
			{Name: "ComfyUI-Manager", Repo: "https://github.com/Comfy-Org/ComfyUI-Manager.git",
				Channel: "main", Path: "custom_nodes/ComfyUI-Manager", Commit: suiteShaB},
		},
	}
}

func TestEngineInstallAdoptWritesStateAndModelPaths(t *testing.T) {
	cfg := suiteTestConfig(t)
	g := newFakeSuiteGit()
	sh := &fakeSuiteShell{t: t}
	useSuiteFakes(t, g, sh)
	tree := t.TempDir()
	suiteTestTree(t, tree)
	g.set(tree, suiteShaA, "master", "https://github.com/comfyanonymous/ComfyUI.git", map[string]string{})
	mgr := filepath.Join(tree, "custom_nodes", "ComfyUI-Manager")
	g.set(mgr, suiteShaB, "main", "https://github.com/Comfy-Org/ComfyUI-Manager.git", map[string]string{})
	port := freePort(t)
	code, out, errOut := runCLI(t, "engine", "install", "comfyui", "--adopt", tree,
		"--config", cfg, "--port", strconv.Itoa(port))
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	st := loadStateFile(t, cfg)
	if len(st.Installs) != 1 {
		t.Fatalf("installs = %d, want 1", len(st.Installs))
	}
	inst := st.Installs[0]
	if inst.Name != "comfyui" || inst.Path != tree || inst.Port != port {
		t.Errorf("install = %+v", inst)
	}
	if inst.Venv != ".venv" {
		t.Errorf("venv = %q, want .venv", inst.Venv)
	}
	if len(inst.Components) != 2 || inst.Components[0].Name != "comfyui" ||
		inst.Components[1].Name != "ComfyUI-Manager" {
		t.Errorf("components = %+v", inst.Components)
	}
	if inst.Components[0].Commit != suiteShaA || inst.Components[1].Commit != suiteShaB {
		t.Errorf("commits = %s / %s", inst.Components[0].Commit, inst.Components[1].Commit)
	}
	raw, err := os.ReadFile(filepath.Join(tree, "extra_model_paths.yaml"))
	if err != nil {
		t.Fatalf("the model paths file is not there: %v", err)
	}
	if !strings.Contains(string(raw), "berth:") {
		t.Errorf("the model paths file lacks the berth section:\n%s", raw)
	}
	for _, want := range []string{"--as comfyui", "--drain-comfyui", "--extra-model-paths-config"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
}

func TestEngineInstallAdoptRefusals(t *testing.T) {
	cfg := suiteTestConfig(t)
	g := newFakeSuiteGit()
	useSuiteFakes(t, g, &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	g.set(tree, suiteShaA, "master", "", map[string]string{})
	g.set(filepath.Join(tree, "custom_nodes", "ComfyUI-Manager"), suiteShaB, "main", "", map[string]string{})
	port := freePort(t)
	code, _, errOut := runCLI(t, "engine", "install", "comfyui", "--adopt", tree,
		"--config", cfg, "--port", strconv.Itoa(port))
	if code != ExitOK {
		t.Fatalf("first install: exit = %d, want 0 (stderr %q)", code, errOut)
	}
	// a second install on the same port is refused
	code, _, errOut = runCLI(t, "engine", "install", "comfyui-2", "--adopt", tree,
		"--config", cfg, "--port", strconv.Itoa(port))
	if code != ExitFail {
		t.Fatalf("port clash: exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "port") {
		t.Errorf("stderr %q does not name the port clash", errOut)
	}
	// and the first install took the name
	code, _, errOut = runCLI(t, "engine", "install", "comfyui", "--adopt", tree,
		"--config", cfg, "--port", strconv.Itoa(freePort(t)))
	if code != ExitFail {
		t.Fatalf("name clash: exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "duplicate name") {
		t.Errorf("stderr %q does not name the duplicate", errOut)
	}
	// a path that is not a checkout is a validation refusal
	nope := t.TempDir()
	code, _, errOut = runCLI(t, "engine", "install", "comfyui-3", "--adopt", nope,
		"--config", cfg, "--port", strconv.Itoa(freePort(t)))
	if code != ExitFail {
		t.Fatalf("not a checkout: exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "main.py") {
		t.Errorf("stderr %q does not name what is missing", errOut)
	}
	// --adopt and --path together are a validation refusal
	code, _, _ = runCLI(t, "engine", "install", "comfyui-4", "--adopt", tree, "--path", tree,
		"--config", cfg)
	if code != ExitFail {
		t.Fatalf("both flags: exit = %d, want %d", code, ExitFail)
	}
}

func TestEngineInstallFreshRunsTheProcedure(t *testing.T) {
	cfg := suiteTestConfig(t)
	g := newFakeSuiteGit()
	sh := &fakeSuiteShell{t: t}
	useSuiteFakes(t, g, sh)
	target := filepath.Join(t.TempDir(), "fresh")
	g.set(target, suiteShaA, "master", "https://github.com/comfyanonymous/ComfyUI.git", map[string]string{})
	g.set(filepath.Join(target, "custom_nodes", "ComfyUI-Manager"), suiteShaB, "main",
		"https://github.com/Comfy-Org/ComfyUI-Manager.git", map[string]string{})
	g.set(filepath.Join(target, "custom_nodes", "deforum-stable"), suiteShaC, "master", "", map[string]string{})
	code, _, errOut := runCLI(t, "engine", "install", "comfyui", "--path", target,
		"--config", cfg, "--port", strconv.Itoa(freePort(t)),
		"--pack", "https://github.com/deforum/deforum-stable")
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	st := loadStateFile(t, cfg)
	if len(st.Installs) != 1 || len(st.Installs[0].Components) != 3 {
		t.Fatalf("install = %+v, want the core, the manager and the pack", st.Installs)
	}
	joined := strings.Join(sh.calls, "\n")
	for _, want := range []string{
		"git clone --branch master https://github.com/comfyanonymous/ComfyUI.git " + target,
		"python3 -m venv .venv",
		"-m pip install -r requirements.txt",
		"git clone https://github.com/deforum/deforum-stable",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("calls %v lack %q", sh.calls, want)
		}
	}
}

func TestEngineInstallStepFailureIsASystemExit(t *testing.T) {
	cfg := suiteTestConfig(t)
	sh := &fakeSuiteShell{t: t, fail: "venv"}
	useSuiteFakes(t, newFakeSuiteGit(), sh)
	code, _, errOut := runCLI(t, "engine", "install", "comfyui", "--path",
		filepath.Join(t.TempDir(), "x"), "--config", cfg)
	if code != ExitSystem {
		t.Fatalf("exit = %d, want %d", code, ExitSystem)
	}
	if !strings.Contains(errOut, "venv") {
		t.Errorf("stderr %q does not name the failed step", errOut)
	}
	if _, err := os.Stat(stateFile(cfg)); !os.IsNotExist(err) {
		t.Error("a failed install must not leave a state file")
	}
}

func TestEngineShowListsTheComponents(t *testing.T) {
	cfg := suiteTestConfig(t)
	tree := t.TempDir()
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui", tree, freePort(t)),
	}})
	code, out, errOut := runCLI(t, "engine", "show", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	for _, want := range []string{"comfyui", "ComfyUI-Manager", short(suiteShaA), short(suiteShaB)} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
	// one install named: just that one
	code, out, errOut = runCLI(t, "engine", "show", "comfyui", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "ComfyUI-Manager") {
		t.Errorf("stdout %q lacks the component", out)
	}
	// a name the state does not hold
	code, _, errOut = runCLI(t, "engine", "show", "nope", "--config", cfg)
	if code != ExitSystem {
		t.Fatalf("exit = %d, want %d", code, ExitSystem)
	}
	if !strings.Contains(errOut, "no install named") {
		t.Errorf("stderr %q does not name the missing install", errOut)
	}
}

// queueServer is a stand-in ComfyUI whose /queue reports the given running
// jobs; the suite's job guard reads it on the install's port.
func queueServer(t *testing.T, running int) int {
	t.Helper()
	jobs := make([]string, 0, running)
	for i := 0; i < running; i++ {
		jobs = append(jobs, fmt.Sprintf(`["job-%d",{},0,1]`, i))
	}
	body := fmt.Sprintf(`{"queue_running":[%s],"queue_pending":[]}`, strings.Join(jobs, ","))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/queue" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestEngineUpdateMovesThePins(t *testing.T) {
	cfg := suiteTestConfig(t)
	g := newFakeSuiteGit()
	useSuiteFakes(t, g, &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	port := freePort(t)
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui", tree, port),
	}})
	g.set(tree, suiteShaA, "master", "", map[string]string{"origin/master": suiteShaC})
	mgr := filepath.Join(tree, "custom_nodes", "ComfyUI-Manager")
	g.set(mgr, suiteShaB, "main", "", map[string]string{"origin/main": suiteShaD})
	code, out, errOut := runCLI(t, "engine", "update", "comfyui", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	for _, want := range []string{
		fmt.Sprintf("comfyui: %s -> %s", short(suiteShaA), short(suiteShaC)),
		fmt.Sprintf("ComfyUI-Manager: %s -> %s", short(suiteShaB), short(suiteShaD)),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout %q lacks %q", out, want)
		}
	}
	st := loadStateFile(t, cfg)
	core, mgrC := st.Installs[0].Components[0], st.Installs[0].Components[1]
	if core.Commit != suiteShaC || core.Previous != suiteShaA {
		t.Errorf("core = %s / %s, want %s / %s", core.Commit, core.Previous, suiteShaC, suiteShaA)
	}
	if mgrC.Commit != suiteShaD || mgrC.Previous != suiteShaB {
		t.Errorf("manager = %s / %s, want %s / %s", mgrC.Commit, mgrC.Previous, suiteShaD, suiteShaB)
	}
}

func TestEngineUpdateRefusesARunningQueue(t *testing.T) {
	cfg := suiteTestConfig(t)
	useSuiteFakes(t, newFakeSuiteGit(), &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	port := queueServer(t, 2)
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui", tree, port),
	}})
	code, _, errOut := runCLI(t, "engine", "update", "comfyui", "--config", cfg)
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d", code, ExitFail)
	}
	if !strings.Contains(errOut, "2 jobs running") {
		t.Errorf("stderr %q does not name the running jobs", errOut)
	}
	st := loadStateFile(t, cfg)
	if st.Installs[0].Components[0].Commit != suiteShaA {
		t.Error("the pins moved though the queue was running")
	}
}

func TestEngineUpdateRefusesOnlyTheDirtyComponent(t *testing.T) {
	cfg := suiteTestConfig(t)
	g := newFakeSuiteGit()
	useSuiteFakes(t, g, &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	port := freePort(t)
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui", tree, port),
	}})
	g.dirty[tree] = true // the core gained local changes after the state was written
	g.set(tree, suiteShaA, "master", "", map[string]string{"origin/master": suiteShaC})
	mgr := filepath.Join(tree, "custom_nodes", "ComfyUI-Manager")
	g.set(mgr, suiteShaB, "main", "", map[string]string{"origin/main": suiteShaD})
	code, out, errOut := runCLI(t, "engine", "update", "comfyui", "--config", cfg)
	if code != ExitFail {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(out+errOut, "dirty") {
		t.Errorf("the report does not name the dirty component:\n%s\n%s", out, errOut)
	}
	if !strings.Contains(out, "ComfyUI-Manager") {
		t.Errorf("the clean component was not updated: %q", out)
	}
	st := loadStateFile(t, cfg)
	core, mgrC := st.Installs[0].Components[0], st.Installs[0].Components[1]
	if core.Commit != suiteShaA {
		t.Error("the dirty core moved")
	}
	if !core.Dirty {
		t.Error("the state does not record the core as dirty")
	}
	if mgrC.Commit != suiteShaD {
		t.Error("the clean component did not move")
	}
}

func TestEngineRollbackSwapsThePinsBack(t *testing.T) {
	cfg := suiteTestConfig(t)
	g := newFakeSuiteGit()
	useSuiteFakes(t, g, &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	port := freePort(t)
	inst := suiteTestInstall("comfyui", tree, port)
	inst.Components[0].Commit = suiteShaC
	inst.Components[0].Previous = suiteShaA
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{inst}})
	g.set(tree, suiteShaC, "master", "", map[string]string{})
	code, out, errOut := runCLI(t, "engine", "rollback", "comfyui", "--component", "comfyui", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, fmt.Sprintf("comfyui: %s -> %s", short(suiteShaC), short(suiteShaA))) {
		t.Errorf("stdout %q lacks the swap", out)
	}
	st := loadStateFile(t, cfg)
	core := st.Installs[0].Components[0]
	if core.Commit != suiteShaA || core.Previous != suiteShaC {
		t.Errorf("core = %s / %s, want %s / %s", core.Commit, core.Previous, suiteShaA, suiteShaC)
	}
	// a component without a previous commit is refused
	code, _, errOut = runCLI(t, "engine", "rollback", "comfyui", "--component", "ComfyUI-Manager", "--config", cfg)
	if code != ExitFail {
		t.Fatalf("no previous: exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "no previous commit") {
		t.Errorf("stderr %q does not name the missing previous commit", errOut)
	}
	// and without --component the command refuses
	code, _, _ = runCLI(t, "engine", "rollback", "comfyui", "--config", cfg)
	if code != ExitFail {
		t.Fatalf("missing --component: exit = %d, want %d", code, ExitFail)
	}
}

func TestEngineRemoveDropsTheEntry(t *testing.T) {
	cfg := suiteTestConfig(t)
	useSuiteFakes(t, newFakeSuiteGit(), &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui", tree, freePort(t)),
	}})
	code, out, errOut := runCLI(t, "engine", "remove", "comfyui", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	st := loadStateFile(t, cfg)
	if len(st.Installs) != 0 {
		t.Errorf("installs = %+v, want none", st.Installs)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Errorf("without --delete the directory must stay: %v", err)
	}
	if !strings.Contains(out, "stay where they are") {
		t.Errorf("stdout %q does not say the directory stays", out)
	}
	// --delete without --yes is refused and touches nothing
	code, _, errOut = runCLI(t, "engine", "remove", "comfyui-2", "--config", cfg)
	if code != ExitFail {
		t.Fatalf("unknown name: exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui-2", tree, freePort(t)),
	}})
	code, _, errOut = runCLI(t, "engine", "remove", "comfyui-2", "--delete", "--config", cfg)
	if code != ExitFail {
		t.Fatalf("--delete without --yes: exit = %d, want %d (stderr %q)", code, ExitFail, errOut)
	}
	if !strings.Contains(errOut, "--yes") {
		t.Errorf("stderr %q does not name the consent flag", errOut)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Errorf("the refused removal touched the directory: %v", err)
	}
	// with the consent the directory goes
	code, _, errOut = runCLI(t, "engine", "remove", "comfyui-2", "--delete", "--yes", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("--delete --yes: exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Errorf("the directory %s survived --delete --yes", tree)
	}
	st = loadStateFile(t, cfg)
	if len(st.Installs) != 0 {
		t.Errorf("installs = %+v, want none", st.Installs)
	}
}

// TestEngineCheckSeesTheSuiteInstall is the end of the wiring: the state file
// beside the config is enough for engine check to read the derived engine.
func TestEngineCheckSeesTheSuiteInstall(t *testing.T) {
	cfg := suiteTestConfig(t)
	useSuiteFakes(t, newFakeSuiteGit(), &fakeSuiteShell{t: t})
	tree := t.TempDir()
	suiteTestTree(t, tree)
	port := queueServer(t, 0) // an idle ComfyUI: the queue is empty
	writeStateFile(t, cfg, suite.State{Installs: []suite.Install{
		suiteTestInstall("comfyui", tree, port),
	}})
	code, out, errOut := runCLI(t, "engine", "check", "--config", cfg)
	if code != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if !strings.Contains(out, "comfyui") || !strings.Contains(out, "up") {
		t.Errorf("stdout %q does not show the suite install answering", out)
	}
}
