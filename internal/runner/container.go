// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The container steps of `berth run --container NAME` and `--compose FILE`.
// The wrapper runs the runtime CLI, so the engine's GPU processes are
// parented by containerd-shim and the process tree cannot reach them: the
// wrapper resolves the engine's containers to their full IDs, names them on
// the lease, and on a drain stops them through the runtime CLI. Only the
// containers this command started are ever stopped: a Compose project is
// stopped with `compose stop`, never `down`, which would delete containers
// and networks the operator owns.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

// containerRef carries the resolved containers between the resolver
// goroutine, the re-attach dialogue and the drain.
type containerRef struct {
	mu  sync.Mutex
	rt  string
	ids []string
}

func (c *containerRef) set(rt string, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rt, c.ids = rt, slices.Clone(ids)
}

func (c *containerRef) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ids)
}

func (c *containerRef) runtime() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rt
}

// lookPath finds a runtime CLI on PATH. Tests name the runtimes a machine
// has instead of the ones installed here.
var lookPath = exec.LookPath

// runtimes lists the runtime CLIs on PATH, docker first. Either runtime can
// hold the engine's containers, and the one that answers is the one that
// owns them.
func runtimes() []string {
	var out []string
	for _, rt := range []string{"docker", "podman"} {
		if _, err := lookPath(rt); err == nil {
			out = append(out, rt)
		}
	}
	return out
}

// firstRuntime is the runtime the drain stops with when the resolver never
// answered: docker, or podman where there is no docker. "" when neither is
// installed, which leaves the process group as the only thing left to stop.
func firstRuntime() string {
	if rs := runtimes(); len(rs) > 0 {
		return rs[0]
	}
	return ""
}

// inspect resolves the container name with the runtime CLI: docker, then
// podman when docker did not answer. A name the runtime does not know yet
// (the wrapper has barely started `docker run`) is an error, and the
// resolver retries.
func inspect(o Options, name string) (runtime, id string, err error) {
	var last error
	for _, rt := range runtimes() {
		out, err := o.CLI([]string{rt, "inspect", "-f", "{{.Id}}", name}, 10*time.Second)
		if err != nil {
			last = fmt.Errorf("%s inspect: %w", rt, err)
			continue
		}
		if id = strings.TrimSpace(out); id != "" {
			return rt, id, nil
		}
	}
	if len(runtimes()) == 0 {
		return "", "", errors.New("no docker or podman on PATH")
	}
	return "", "", last
}

// composePS lists the project's container IDs with the runtime CLI, stopped
// containers included: a service that is between two runs is still part of
// the engine's footprint. The runtime that answers with a non-empty list
// owns the project.
func composePS(o Options, path string) (runtime string, ids []string, err error) {
	var last error
	for _, rt := range runtimes() {
		out, err := o.CLI(composePSArgv(rt, path), 10*time.Second)
		if err != nil {
			last = fmt.Errorf("%s compose ps: %w", rt, err)
			continue
		}
		if ids = idLines(out); len(ids) > 0 {
			return rt, ids, nil
		}
	}
	if len(runtimes()) == 0 {
		return "", nil, errors.New("no docker or podman on PATH")
	}
	return "", nil, last
}

// composePSArgv is the runtime's own answer to "which containers is this
// Compose file running right now".
func composePSArgv(runtime, path string) []string {
	return []string{runtime, "compose", "-f", path, "ps", "-q", "--all"}
}

// idLines is the IDs a runtime listing printed, one per line. Fields also
// swallows the CR a Windows runtime ends its lines with.
func idLines(out string) []string {
	var ids []string
	for _, line := range strings.Fields(out) {
		if line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}

// resolve asks the runtime for the containers this run's engine is in: the
// whole Compose project, or the one container the operator named. Either
// way the lease holds a set, so the rest of the runner has one path.
func resolve(o Options) (runtime string, ids []string, err error) {
	if o.Compose != "" {
		return composePS(o, o.Compose)
	}
	rt, id, err := inspect(o, o.Container)
	if err != nil {
		return "", nil, err
	}
	return rt, []string{id}, nil
}

// containerTarget is what the run calls what it resolves in its own
// notices: the Compose file, or the container name the operator gave.
func (o Options) containerTarget() string {
	if o.Compose != "" {
		return o.Compose
	}
	return o.Container
}

// resolveContainers names the engine's containers on the lease. It polls to
// the end of the run: a container that restarts comes back under a new ID,
// and the next poll sends the set again. It sends over the lease's own line,
// so a run that lost its daemon and re-attached keeps telling the new daemon
// what it owns.
func resolveContainers(o Options, line *leaseLine, cmd *exec.Cmd, done <-chan struct{}, ref *containerRef, say func(string, ...any)) {
	every := o.pollEvery()
	lastErr, sent := "", []string(nil)
	for {
		select {
		case <-done:
			return
		case <-time.After(every):
		}
		rt, ids, err := resolve(o)
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				say("no containers for %s yet (%v); retrying", o.containerTarget(), err)
				lastErr = msg
			}
			continue
		}
		lastErr = ""
		ref.set(rt, ids)
		if slices.Equal(ids, sent) {
			continue // nothing changed since the last poll
		}
		if err := line.send(proto.Msg{Op: proto.OpAttach, PID: cmd.Process.Pid, Containers: ids}); err != nil {
			continue // no daemon: the re-attach names them to the new one
		}
		sent = ids
		say("containers for %s: %s", o.containerTarget(), strings.Join(ids, " "))
	}
}

// stopArgv is the `docker stop -t <grace seconds> ID` (or podman's) that
// hands a container engine's drain to the runtime CLI. It stops the full ID
// the resolver answered, never the bare name: a name could have been taken
// over by a container Berth never started.
func stopArgv(runtime, id string, grace time.Duration) []string {
	return []string{runtime, "stop", "-t", graceSeconds(grace), id}
}

// composeStopArgv is `docker compose -f PATH stop -t <grace seconds>` (or
// podman's): it stops every service of the project, and never `down`, which
// would delete the containers and networks the operator owns.
func composeStopArgv(runtime, path string, grace time.Duration) []string {
	return []string{runtime, "compose", "-f", path, "stop", "-t", graceSeconds(grace)}
}

func graceSeconds(grace time.Duration) string {
	return strconv.FormatInt(int64(grace/time.Second), 10)
}

// stopContainers hands the drain to the runtime CLI: the Compose project, or
// the one container once the resolver answered with its ID (before that
// there is no ID to stop, so the caller's group signals run as they always
// have). true means the runtime stopped the engine.
func stopContainers(o Options, say func(string, ...any)) bool {
	rt := firstRuntime()
	if o.container != nil {
		if got := o.container.runtime(); got != "" {
			rt = got
		}
	}
	if rt == "" {
		return false
	}
	var argv []string
	if o.Compose != "" {
		argv = composeStopArgv(rt, o.Compose, o.Grace)
	} else {
		var ids []string
		if o.container != nil {
			ids = o.container.list()
		}
		if len(ids) != 1 {
			return false // nothing resolved yet
		}
		argv = stopArgv(rt, ids[0], o.Grace)
	}
	if _, err := o.CLI(argv, o.Grace+15*time.Second); err != nil {
		say("stopping %s failed (%v); falling back to the process group", o.containerTarget(), err)
		return false
	}
	return true
}

// execCLI runs one docker or podman command under a deadline and gives back
// its output. A hung runtime daemon leaks nothing: every call ends by the
// timeout (inspect 10 s; stop the drain's grace plus 15 s, set by the
// caller).
func execCLI(argv []string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
