// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The container steps of `berth run --container NAME`. The wrapper runs the
// runtime CLI, so the engine's GPU processes are parented by containerd-shim
// and the process tree cannot reach them: the wrapper resolves the name to
// the full ID, names it on the lease, and on a drain stops the container
// through the runtime CLI. Only the container this command started is ever
// stopped.

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

// containerRef carries the resolved container between the resolver
// goroutine, the re-attach dialogue and the drain.
type containerRef struct {
	mu  sync.Mutex
	rt  string
	cid string
}

func (c *containerRef) set(rt, cid string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rt, c.cid = rt, cid
}

func (c *containerRef) id() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cid
}

func (c *containerRef) runtime() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rt
}

// runtimes lists the runtime CLIs on PATH, docker first. Either runtime can
// hold the container, and the one that answers is the one that owns it.
func runtimes() []string {
	var out []string
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err == nil {
			out = append(out, rt)
		}
	}
	return out
}

// inspect resolves the container name with the runtime CLI: docker, then
// podman when docker did not answer. A name the runtime does not know yet
// (the wrapper has barely started `docker run`) is an error, and the
// resolver retries.
func inspect(o Options, name string) (runtime, id string, err error) {
	var last error
	for _, rt := range runtimes() {
		out, err := o.CLI([]string{rt, "inspect", "-f", "{{.Id}}", name})
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

// resolveContainer resolves the operator's container name to its full ID
// and names it on the lease. It retries every Retry until the name resolves
// or the engine exits. The lease's connection may have been replaced by
// then; the re-attach loop sends the ID to the new connection.
func resolveContainer(o Options, conn *proto.Conn, cmd *exec.Cmd, done <-chan struct{}, cid *containerRef, say func(string, ...any)) {
	every := o.pollEvery()
	last := ""
	for {
		select {
		case <-done:
			return
		case <-time.After(every):
		}
		rt, id, err := inspect(o, o.Container)
		if err != nil || id == "" {
			if msg := fmt.Sprint(err); msg != last {
				say("container %s not found yet (%v); retrying", o.Container, err)
				last = msg
			}
			continue
		}
		cid.set(rt, id)
		for {
			if err := conn.Send(proto.Msg{Op: proto.OpAttach, PID: cmd.Process.Pid, Container: id}); err == nil {
				say("container %s is %s", o.Container, id)
				return
			}
			select {
			case <-done:
				return
			case <-time.After(every):
			}
		}
	}
}

// stopArgv is the `docker stop -t <grace seconds> NAME` (or podman's) that
// hands a container engine's drain to the runtime CLI.
func stopArgv(runtime, name string, grace time.Duration) []string {
	return []string{runtime, "stop", "-t", strconv.FormatInt(int64(grace/time.Second), 10), name}
}

// execCLI runs one docker or podman command and gives back its output.
func execCLI(argv []string) (string, error) {
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}
