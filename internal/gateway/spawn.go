// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// The gateway starts each engine as its own `berth run` process, not in
// process: the run keeps the lease, drains and reattaches on its own, and
// outlives the gateway that asked for it. This file builds that command
// and reads its output; adopt.go decides what a restart may reuse.
package gateway

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/runner"
)

// spawnRun starts the run process for one engine, records it, and waits
// for its end, feeding the output the tail keeps.
func (g *Gateway) spawnRun(name string, s *started, o runner.Options) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	full := append([]string{exe}, runArgv(o)...)
	full = append(full, "--")
	full = append(full, o.Argv...)
	full = scopeArgv(name, full)
	cmd := exec.Command(full[0], full[1:]...)
	detach(cmd)
	cmd.Stdout = io.MultiWriter(os.Stdout, s.output)
	cmd.Stderr = &watchWriter{w: io.MultiWriter(os.Stderr, s.stderr, s.output),
		see: func(line string) {
			if lease, ok := grantedLease(line); ok {
				g.setLease(name, lease)
			}
		}}
	// A grandchild that holds the output pipes after the run ends must
	// not hold the wait.
	cmd.WaitDelay = 2 * time.Second
	g.log().Info("gateway starting engine", "engine", name)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	g.putRecord(engineRec{Engine: name, PID: cmd.Process.Pid,
		Start: procs.System{}.StartTime(cmd.Process.Pid), Started: time.Now()})
	code := exitCodeOf(cmd, cmd.Wait())
	g.dropRecord(name)
	return code, nil
}

// runArgv renders the options as `berth run`'s own flags; the engine's
// command follows the caller's "--".
func runArgv(o runner.Options) []string {
	args := []string{"run", "--socket", o.Socket, "--as", o.Engine}
	if o.Group != "" {
		args = append(args, "--group", o.Group)
	}
	if o.Priority != "" {
		args = append(args, "--priority", o.Priority)
	}
	if o.Needs != 0 {
		args = append(args, "--needs-mib", strconv.FormatUint(o.Needs>>20, 10))
	}
	if o.Wait != 0 {
		args = append(args, "--wait", o.Wait.String())
	}
	if o.Pinned {
		args = append(args, "--pinned")
	}
	if o.DrainPost != "" {
		args = append(args, "--drain-post", o.DrainPost)
	}
	if o.DrainSlots != "" {
		args = append(args, "--drain-slots", o.DrainSlots)
	}
	if o.DrainComfyUI != "" {
		args = append(args, "--drain-comfyui", o.DrainComfyUI)
	}
	if o.KeepComfyUI {
		args = append(args, "--keep-comfyui-up")
	}
	if o.Grace != 0 {
		args = append(args, "--grace", o.Grace.String())
	}
	return args
}

// exitCodeOf is the run process's exit code, the engine's status among
// them. A WaitDelay error is a clean exit: a grandchild held the output
// pipes, nothing worse.
func exitCodeOf(cmd *exec.Cmd, err error) int {
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 1
	}
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

// grantedLease is the lease ID in a "granted" line of a run's output.
func grantedLease(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "berth: granted ")
	if !ok {
		return "", false
	}
	if i := strings.Index(rest, ","); i >= 0 {
		rest = rest[:i]
	}
	return rest, true
}

// watchWriter feeds w and watches the complete lines going through, so
// the record learns the lease the moment the run is granted one.
type watchWriter struct {
	w    io.Writer
	see  func(line string)
	rest []byte
}

func (t *watchWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	t.rest = append(t.rest, p...)
	for {
		i := bytes.IndexByte(t.rest, '\n')
		if i < 0 {
			break
		}
		t.see(string(t.rest[:i]))
		t.rest = t.rest[i+1:]
	}
	if len(t.rest) > 4<<10 { // no line ends in sight; drop the head
		t.rest = nil
	}
	return n, err
}
