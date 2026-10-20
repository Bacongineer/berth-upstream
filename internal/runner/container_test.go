// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package runner

// The container steps: resolve the name with the runtime CLI, stop the
// container on a drain by its full ID with the runtime that answered, and
// fall back to the process group when the stop fails or before any
// resolution. The CLI is faked and the argv is the assertion; no test
// touches a real docker.

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

const containerID = "9b5f6c4d3e2a1b0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f"

// A container engine's drain stops the container with the runtime that
// resolved it, by its full ID.
func TestDrainStopsTheContainer(t *testing.T) {
	var argvs [][]string
	var timeouts []time.Duration
	o := Options{Engine: "vllm", Container: "vllm", Grace: time.Second,
		container: &containerRef{rt: "docker", cid: containerID},
		CLI: func(argv []string, timeout time.Duration) (string, error) {
			argvs = append(argvs, argv)
			timeouts = append(timeouts, timeout)
			return "", nil
		}}
	drain(o, nil, make(chan struct{}), make(chan struct{}), "test", func(string, ...any) {})
	if len(argvs) != 1 {
		t.Fatalf("commands = %v, want one stop", argvs)
	}
	if strings.Join(argvs[0], " ") != "docker stop -t 1 "+containerID {
		t.Fatalf("stop argv = %v", argvs[0])
	}
	if got := timeouts[0]; got != 16*time.Second {
		t.Fatalf("stop timeout = %s, want the grace plus 15 s", got)
	}
}

// The runtime that resolved is the runtime that stops: a podman resolution
// stops through podman.
func TestDrainStopsWithTheRuntimeThatAnswered(t *testing.T) {
	var argvs [][]string
	o := Options{Engine: "vllm", Container: "vllm", Grace: time.Second,
		container: &containerRef{rt: "podman", cid: containerID},
		CLI: func(argv []string, timeout time.Duration) (string, error) {
			argvs = append(argvs, argv)
			return containerID + "\n", nil
		}}
	drain(o, nil, make(chan struct{}), make(chan struct{}), "test", func(string, ...any) {})
	if len(argvs) != 1 {
		t.Fatalf("commands = %v, want one stop", argvs)
	}
	if strings.Join(argvs[0], " ") != "podman stop -t 1 "+containerID {
		t.Fatalf("stop argv = %v", argvs[0])
	}
}

// Before the name resolved there is no ID to stop: no runtime command runs,
// and the drain falls back to today's group signals. The bare name is never
// the target.
func TestDrainBeforeResolutionFallsBack(t *testing.T) {
	cmd, waited := startSleep(t)
	var argvs [][]string
	o := Options{Engine: "sleep", Container: "vllm", Grace: 100 * time.Millisecond,
		CLI: func(argv []string, timeout time.Duration) (string, error) {
			argvs = append(argvs, argv)
			return "", nil
		}}
	drain(o, cmd, make(chan struct{}), make(chan struct{}), "test", func(string, ...any) {})
	if len(argvs) != 0 {
		t.Fatalf("commands = %v, want none before resolution", argvs)
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
	cmd, waited := startSleep(t)
	var argvs [][]string
	o := Options{Engine: "sleep", Container: "vllm", Grace: 100 * time.Millisecond,
		container: &containerRef{rt: "docker", cid: containerID},
		CLI: func(argv []string, timeout time.Duration) (string, error) {
			argvs = append(argvs, argv)
			if argv[1] == "stop" {
				return "cannot stop", errors.New("docker stop failed")
			}
			return containerID + "\n", nil
		}}
	done := make(chan struct{})
	drain(o, cmd, done, make(chan struct{}), "test", func(string, ...any) {})
	if strings.Join(argvs[len(argvs)-1], " ") != "docker stop -t 0 "+containerID {
		t.Fatalf("stop argv = %v", argvs)
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

// resolveContainer retries until the runtime knows the name, then attaches
// the full ID to the lease; the inspect runs under its own deadline.
func TestResolveContainerRetriesThenAttaches(t *testing.T) {
	client, serverRaw := net.Pipe()
	defer client.Close()
	defer serverRaw.Close()
	conn := proto.NewConn(client)
	msgs := make(chan proto.Msg, 4)
	go func() {
		for {
			m, err := proto.NewConn(serverRaw).Recv()
			if err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()

	n := 0
	var seen time.Duration
	o := Options{Container: "vllm", Retry: time.Millisecond, CLI: func(argv []string, timeout time.Duration) (string, error) {
		n++
		seen = timeout
		if n < 3 {
			return "", errors.New("no such container")
		}
		return containerID + "\n", nil
	}}
	cmd := &exec.Cmd{Process: &os.Process{Pid: 1234}}
	var cid containerRef
	resolveContainer(o, conn, cmd, make(chan struct{}), &cid, func(string, ...any) {})

	if id := cid.id(); id != containerID {
		t.Fatalf("resolved = %q", id)
	}
	if seen != 10*time.Second {
		t.Fatalf("inspect timeout = %s, want 10 s", seen)
	}
	m := <-msgs
	if m.Op != proto.OpAttach || m.PID != 1234 || m.Container != containerID {
		t.Fatalf("attach = %+v", m)
	}
}
