// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package runner

// The container steps: name the engine's containers with the runtime CLI
// (one container by name, a Compose project by its whole set), stop them on
// a drain through the runtime that answered, and fall back to the process
// group when the stop fails or before anything resolved. The CLI is faked
// and the argv is the assertion; no test touches a real docker.

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

const (
	containerID = "9b5f6c4d3e2a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f"
	sidecarID   = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f809"
	composeFile = "/srv/tagger/compose.yaml"
	psArgv      = " compose -f " + composeFile + " ps -q --all"
)

// onlyRuntimes names the runtime CLIs this test's machine has.
func onlyRuntimes(t *testing.T, have ...string) {
	t.Helper()
	yes := map[string]bool{}
	for _, rt := range have {
		yes[rt] = true
	}
	prev := lookPath
	lookPath = func(name string) (string, error) {
		if yes[name] {
			return "/usr/bin/" + name, nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = prev })
}

// fakeCLI is a runtime CLI that answers from answer and keeps what the run
// asked it, so a resolver still polling never races the test's own read.
type fakeCLI struct {
	mu       sync.Mutex
	n        int
	argvs    []string
	timeouts []time.Duration
	answer   func(n int, argv []string) (string, error)
}

func (f *fakeCLI) run(argv []string, timeout time.Duration) (string, error) {
	f.mu.Lock()
	f.n++
	n := f.n
	f.argvs = append(f.argvs, strings.Join(argv, " "))
	f.timeouts = append(f.timeouts, timeout)
	f.mu.Unlock()
	if f.answer == nil {
		return "", nil
	}
	return f.answer(n, argv)
}

func (f *fakeCLI) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.argvs)
}

func (f *fakeCLI) deadline(i int) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.timeouts[i]
}

// resolver is a resolveContainers under test: what it sends the lease lands
// on msgs, and it is stopped when the test ends.
type resolver struct {
	msgs <-chan proto.Msg
	ref  containerRef
}

func startResolver(t *testing.T, o Options) *resolver {
	t.Helper()
	client, serverRaw := net.Pipe()
	t.Cleanup(func() {
		client.Close()
		serverRaw.Close()
	})
	daemon := proto.NewConn(serverRaw)
	msgs := make(chan proto.Msg, 8)
	go func() {
		for {
			m, err := daemon.Recv()
			if err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	r := &resolver{msgs: msgs}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		resolveContainers(o, newLine(proto.NewConn(client)), &exec.Cmd{Process: &os.Process{Pid: 1234}},
			done, &r.ref, func(string, ...any) {})
	}()
	// Stopping waits for the last poll: the next test to name its runtimes
	// must not race this one still reading them.
	t.Cleanup(func() {
		close(done)
		<-exited
	})
	return r
}

// The resolve of a Compose engine is the project's own listing, stopped
// containers included, under its own deadline. A sidecar that appears
// between two polls arrives as a new set.
func TestResolveComposePollsTheProjectAndSendsEachSet(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{answer: func(n int, argv []string) (string, error) {
		if n == 1 {
			return containerID + "\n", nil // the sidecar has no container yet
		}
		return containerID + "\n" + sidecarID + "\n", nil
	}}
	r := startResolver(t, Options{Compose: composeFile, Retry: time.Millisecond, CLI: cli.run})

	first, second := <-r.msgs, <-r.msgs
	if got := strings.Join(first.Containers, ","); got != containerID {
		t.Fatalf("first attach = %q, want the one container up so far", got)
	}
	if got := strings.Join(second.Containers, ","); got != containerID+","+sidecarID {
		t.Fatalf("second attach = %q, want the set once the sidecar exists", got)
	}
	if second.Op != proto.OpAttach || second.PID != 1234 {
		t.Fatalf("attach = %+v, want the engine's pid on OpAttach", second)
	}
	seen := cli.seen()
	want := "docker" + psArgv
	if seen[0] != want || seen[1] != want {
		t.Fatalf("resolve argv = %v, want the project listed on every poll", seen[:2])
	}
	if got := cli.deadline(0); got != 10*time.Second {
		t.Fatalf("resolve deadline = %s, want 10 s", got)
	}
	if rt := r.ref.runtime(); rt != "docker" {
		t.Fatalf("runtime = %q, want the one that answered", rt)
	}
}

// An unchanged set is not sent twice: the polls between two changes say
// nothing.
func TestResolveComposeSendsOnlyAChangedSet(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{answer: func(int, []string) (string, error) {
		return containerID + "\n" + sidecarID + "\r\n", nil
	}}
	r := startResolver(t, Options{Compose: composeFile, Retry: time.Millisecond, CLI: cli.run})

	if got := strings.Join((<-r.msgs).Containers, ","); got != containerID+","+sidecarID {
		t.Fatalf("attach = %q", got)
	}
	time.Sleep(200 * time.Millisecond) // hundreds of polls on the same set
	select {
	case m := <-r.msgs:
		t.Fatalf("sent again: %+v, want one attach while the set holds", m)
	default:
	}
	if n := len(cli.seen()); n < 10 {
		t.Fatalf("resolves = %d, want the project polled again and again", n)
	}
}

// podman holds the project when docker does not answer it.
func TestResolveComposeFallsBackToPodman(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{answer: func(n int, argv []string) (string, error) {
		if argv[0] == "docker" {
			return "no such file or directory", errors.New("docker compose ps: exit status 1")
		}
		return containerID + "\n", nil
	}}
	r := startResolver(t, Options{Compose: composeFile, Retry: time.Millisecond, CLI: cli.run})

	if got := strings.Join((<-r.msgs).Containers, ","); got != containerID {
		t.Fatalf("attach = %q, want podman's answer on the lease", got)
	}
	seen := cli.seen()
	if want := "docker" + psArgv; seen[0] != want {
		t.Fatalf("resolve argv = %q, want %q first", seen[0], want)
	}
	if want := "podman" + psArgv; seen[1] != want {
		t.Fatalf("resolve argv = %q, want %q once docker refused", seen[1], want)
	}
}

// A Compose engine is stopped by the runtime that resolved it, and the drain
// waits on the project's own grace.
func TestDrainStopsTheComposeProjectWithTheRuntimeThatAnswered(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	for _, rt := range []string{"docker", "podman"} {
		cli := &fakeCLI{}
		o := Options{Engine: "pet-tagger", Compose: composeFile, Grace: time.Second,
			container: &containerRef{rt: rt, ids: []string{containerID, sidecarID}},
			CLI:       cli.run}
		drain(o, nil, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
		want := rt + " compose -f " + composeFile + " stop -t 1"
		if seen := cli.seen(); len(seen) != 1 || seen[0] != want {
			t.Fatalf("commands = %v, want just %q", seen, want)
		}
		if got := cli.deadline(0); got != 16*time.Second {
			t.Fatalf("stop deadline = %s, want the grace plus 15 s", got)
		}
	}
}

// A container engine's drain stops the container with the runtime that
// resolved it, by its full ID.
func TestDrainStopsTheContainer(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{}
	o := Options{Engine: "vllm", Container: "vllm", Grace: time.Second,
		container: &containerRef{rt: "docker", ids: []string{containerID}},
		CLI:       cli.run}
	drain(o, nil, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
	if seen := cli.seen(); len(seen) != 1 || seen[0] != "docker stop -t 1 "+containerID {
		t.Fatalf("commands = %v, want one stop of the full ID", cli.seen())
	}
	if got := cli.deadline(0); got != 16*time.Second {
		t.Fatalf("stop deadline = %s, want the grace plus 15 s", got)
	}
}

// The runtime that resolved is the runtime that stops: a podman resolution
// stops through podman.
func TestDrainStopsWithTheRuntimeThatAnswered(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{}
	o := Options{Engine: "vllm", Container: "vllm", Grace: time.Second,
		container: &containerRef{rt: "podman", ids: []string{containerID}},
		CLI:       cli.run}
	drain(o, nil, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
	if seen := cli.seen(); len(seen) != 1 || seen[0] != "podman stop -t 1 "+containerID {
		t.Fatalf("commands = %v, want one podman stop", seen)
	}
}

// A Compose engine's drain stops the whole project with its own SIGTERM to
// every service, and never `down`: that deletes the containers and networks
// the operator owns.
func TestDrainNeverRunsDown(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{}
	o := Options{Engine: "pet-tagger", Compose: composeFile, Grace: time.Second,
		container: &containerRef{rt: "docker", ids: []string{containerID, sidecarID}},
		CLI:       cli.run}
	drain(o, nil, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
	seen := cli.seen()
	for _, argv := range seen {
		if strings.Contains(argv, "down") {
			t.Fatalf("commands = %v, want no down: it deletes the project's containers", seen)
		}
	}
}

// Before the name resolved there is no ID to stop: no runtime command runs,
// and the drain falls back to today's group signals. The bare name is never
// the target.
func TestDrainBeforeResolutionFallsBack(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cmd, waited := startSleep(t)
	cli := &fakeCLI{}
	o := Options{Engine: "sleep", Container: "vllm", Grace: 100 * time.Millisecond, CLI: cli.run}
	drain(o, cmd, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
	if seen := cli.seen(); len(seen) != 0 {
		t.Fatalf("commands = %v, want none before resolution", seen)
	}
	select {
	case err := <-waited:
		if !strings.Contains(err.Error(), "terminated") {
			t.Fatalf("engine exit = %v, want the group terminate", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the engine survived the fallback")
	}
}

// A failed container stop falls back to the group signals, as the ladder
// has always run.
func TestDrainContainerStopFailureFallsBack(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cmd, waited := startSleep(t)
	cli := &fakeCLI{answer: func(int, []string) (string, error) {
		return "cannot stop", errors.New("docker stop failed")
	}}
	o := Options{Engine: "sleep", Container: "vllm", Grace: 100 * time.Millisecond,
		container: &containerRef{rt: "docker", ids: []string{containerID}},
		CLI:       cli.run}
	drain(o, cmd, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
	if seen := cli.seen(); len(seen) != 1 || seen[0] != "docker stop -t 0 "+containerID {
		t.Fatalf("commands = %v, want one stop of the full ID", seen)
	}
	select {
	case err := <-waited:
		if !strings.Contains(err.Error(), "terminated") {
			t.Fatalf("engine exit = %v, want the group terminate", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the engine survived the fallback")
	}
}

// A failed project stop falls back the same way: the wrapper's own process
// group is still the last resort.
func TestDrainComposeStopFailureFallsBack(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cmd, waited := startSleep(t)
	cli := &fakeCLI{answer: func(int, []string) (string, error) {
		return "cannot stop", errors.New("docker compose stop failed")
	}}
	o := Options{Engine: "sleep", Compose: composeFile, Grace: 100 * time.Millisecond,
		container: &containerRef{rt: "docker", ids: []string{containerID, sidecarID}},
		CLI:       cli.run}
	drain(o, cmd, newLine(nil), make(chan struct{}), "test", func(string, ...any) {})
	want := "docker compose -f " + composeFile + " stop -t 0"
	if seen := cli.seen(); len(seen) != 1 || seen[0] != want {
		t.Fatalf("commands = %v, want %q", seen, want)
	}
	select {
	case err := <-waited:
		if !strings.Contains(err.Error(), "terminated") {
			t.Fatalf("engine exit = %v, want the group terminate", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the engine survived the fallback")
	}
}

// resolveContainers retries until the runtime knows the name, then attaches
// the full ID as a set of one; the inspect runs under its own deadline.
func TestResolveContainerRetriesThenAttaches(t *testing.T) {
	onlyRuntimes(t, "docker", "podman")

	cli := &fakeCLI{answer: func(n int, argv []string) (string, error) {
		if n < 3 {
			return "", errors.New("no such container")
		}
		return containerID + "\n", nil
	}}
	r := startResolver(t, Options{Container: "vllm", Retry: time.Millisecond, CLI: cli.run})

	m := <-r.msgs
	if m.Op != proto.OpAttach || m.PID != 1234 ||
		strings.Join(m.Containers, ",") != containerID {
		t.Fatalf("attach = %+v", m)
	}
	if got := cli.deadline(0); got != 10*time.Second {
		t.Fatalf("inspect deadline = %s, want 10 s", got)
	}
	if got := strings.Join(r.ref.list(), ","); got != containerID {
		t.Fatalf("resolved = %q", got)
	}
}
