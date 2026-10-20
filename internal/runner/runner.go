// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package runner is `berth run`: acquire a berth, start the engine, hand
// the berth back by stopping the engine when berthd asks, and exit with the
// engine's status. If berthd is down the engine runs anyway (fail-open), so
// Berth never becomes the reason a model cannot load.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/comfy"
	"github.com/T-Crypt/berth/internal/engines"
	"github.com/T-Crypt/berth/internal/park"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/runs"
	"github.com/T-Crypt/berth/internal/server"
)

type Options struct {
	Socket   string
	Engine   string
	Group    string // the engine family for juggles and usage (llama-swap's models: "llama-swap")
	Priority string
	Needs    uint64
	Pinned   bool // never drained: an app that must not be stopped (Blender, a capture)
	// MinTurn is a minimum turn: while the lease is younger than it after
	// the grant, no request of equal or lower priority drains it, and
	// neither does a higher one; only a hold (a game, a call, berth hold)
	// takes the card out. When the turn ends the normal rules apply; a
	// re-attach starts a fresh turn. 0 = off.
	MinTurn   time.Duration
	Wait      time.Duration
	DrainPost string // asked first on drain, e.g. llama-swap's unload URL
	// DrainSlots is an engine base URL with a llama.cpp-style /slots: on
	// drain, wait until no slot is processing, then stop the engine. For a
	// server with no unload URL (Strata), so a drain never cuts off the
	// request it is serving.
	DrainSlots string
	// DrainComfyUI is a ComfyUI base URL: on drain, wait until its queue
	// has nothing running or pending before any stop step, so a job is
	// never cut off. There is no time limit; a job ends when it ends.
	DrainComfyUI string
	// DrainMax is a ceiling on how long a drain may wait at a boundary (a
	// processing slot, a running ComfyUI job) before it runs the normal
	// stop ladder anyway: a wedged engine that keeps reporting busy holds
	// the card without it. 0 = unbounded, the default, so a long
	// generation is never cut off.
	DrainMax time.Duration
	// Park, when set, is the engine's park adapter: on a drain, after the
	// boundary and before the stop ladder, the runner asks the broker to
	// admit the lease into RAM parking and the adapter moves the weights
	// out of VRAM. A refusal or a failed call falls back to the stop
	// ladder. The engine entry builds it from park = "hook".
	Park *park.Adapter
	// ParkBase is the base URL the park adapter and the resume probe go
	// to.
	ParkBase string
	// ParkProbe is the path the engine answers once its card memory is
	// back after a resume (/v1/models for LLMs, /system_stats for Comfy).
	ParkProbe string
	// Hook marks park = "hook" when the entry gave no adapter: the first
	// drain probes Berth's own park protocol once.
	Hook bool
	// Container is the container the engine runs in (berth run
	// --container NAME, the name of `docker run --name`): the drain stops
	// it through the runtime CLI instead of signalling this process
	// group, because containerd-shim, not this wrapper, parents the
	// engine. Linux-only in this release; elsewhere the flag is accepted
	// and ignored. The container is Berth's because this command started
	// it; nothing else is ever stopped.
	Container string
	// CLI is the runtime CLI exec: every docker or podman inspect and stop
	// runs through it, under the timeout the caller names, and it returns
	// the command's combined output; nil = the real CLI, tests fake it and
	// assert the argv.
	CLI func(argv []string, timeout time.Duration) (string, error)
	// container carries the resolved container (the runtime that answered
	// and the full ID) from the resolver goroutine to the drain; Run sets
	// it, tests that fake the resolution set it directly.
	container  *containerRef
	Grace      time.Duration // per drain step before escalating
	FailClosed bool          // refuse to start when berthd is unreachable
	Retry      time.Duration // how often a run that lost berthd dials it again; 0 = 2s
	Argv       []string
	Stderr     io.Writer
	// Output, when set, gets a copy of the engine's own stdout and stderr
	// (the terminal still gets them too). The gateway keeps its tail, so a
	// start that dies says why in the engine's words, not berth's.
	Output io.Writer
}

// ExitRefused is EX_TEMPFAIL: no berth was granted.
const ExitRefused = 75

// pollEvery is how often a background poll retries: the configured Retry,
// or 2s when unset.
func (o Options) pollEvery() time.Duration {
	if o.Retry <= 0 {
		return 2 * time.Second
	}
	return o.Retry
}

func Run(o Options) int {
	say := func(format string, a ...any) { fmt.Fprintf(o.Stderr, "berth: "+format+"\n", a...) }

	conn, raw, err := server.Dial(o.Socket)
	if err != nil {
		if o.FailClosed {
			say("berthd unreachable (%v); not starting %s", err, o.Engine)
			return ExitRefused
		}
		say("UNPROTECTED: berthd unreachable (%v); starting %s without a lease", err, o.Engine)
		return runUnleased(o, "berthd unreachable at start")
	}
	defer raw.Close()

	if err := conn.Send(proto.Msg{Op: proto.OpAcquire, Engine: o.Engine, Group: o.Group, Priority: o.Priority, Needs: o.Needs, Wait: o.Wait.Seconds(), Pinned: o.Pinned, MinTurn: o.MinTurn.Seconds()}); err != nil {
		say("acquire: %v", err)
		return ExitRefused
	}
	for granted := false; !granted; {
		m, err := conn.Recv()
		if err != nil {
			say("berthd went away while %s was waiting: %v", o.Engine, err)
			return ExitRefused
		}
		switch m.Op {
		case proto.OpWaiting:
			say("waiting: %s", m.Reason)
		case proto.OpRefused:
			say("refused: %s", m.Reason)
			return ExitRefused
		case proto.OpGranted:
			if m.Reason != "" {
				say("granted %s, WARNING: %s", m.Lease, m.Reason)
			} else {
				say("granted %s", m.Lease)
			}
			granted = true
		}
	}

	cmd := command(o.Argv, o.Output)
	if err := cmd.Start(); err != nil {
		say("start %s: %v", o.Argv[0], err)
		return 127
	}
	afterStart(cmd)
	_ = conn.Send(proto.Msg{Op: proto.OpAttach, PID: cmd.Process.Pid})

	done := make(chan struct{})
	go forwardSignals(cmd, done)

	// A container engine resolves its name to the full ID and names it on
	// the lease, so the broker attributes the engine's GPU processes
	// (containerd-shim parents them, not this wrapper). The ID also rides
	// on any re-attach, in case the daemon changes before it resolves.
	if o.CLI == nil {
		o.CLI = execCLI
	}
	var cid containerRef
	o.container = &cid
	if o.Container != "" {
		if containerSupported() {
			go resolveContainer(o, conn, cmd, done, &cid, say)
		} else {
			say("container engines are Linux-only in this release; --container %s ignored", o.Container)
		}
	}

	// line is the lease's live conn and park state; the drain and the
	// resume ride it across reattach loops.
	line := newLine(conn)

	// One drain at a time: a drain parks the engine or stops it, and the
	// daemon's next drain waits for that answer. A token leaves the slot
	// without a drain under way and comes back when it ends.
	drainTicket := make(chan struct{}, 1)
	drainTicket <- struct{}{}

	go func() {
		for {
			err := listen(conn, func(m proto.Msg) {
				switch {
				case m.Op == proto.OpDrain:
					select {
					case <-drainTicket:
					default:
						say("set aside drain %q; the previous one is under way", m.Reason)
						return
					}
					go func() {
						defer func() { drainTicket <- struct{}{} }()
						drain(o, cmd, line, done, m.Reason, say)
					}()
				case m.Op == proto.OpParkOK || m.Op == proto.OpRefused:
					line.note(m)
				case m.Op == proto.OpGranted && m.Resume:
					go line.resume(o, cmd, done, say)
				}
			})
			select {
			case <-done:
				return
			default:
			}
			// The engine keeps running (fail-open) and is on record as
			// unprotected until a daemon answers again and re-attaches it.
			say("UNPROTECTED: lost berthd (%v); %s keeps running without a lease, retrying", err, o.Engine)
			remove := record(o, cmd, runs.ReasonLost)
			var lease string
			conn, raw, lease = reattach(o, cmd, done, &cid)
			remove()
			if conn == nil {
				return
			}
			// Hang up when the engine exits, as the first connection does,
			// so the daemon frees the lease even if this process lives on.
			go func(c net.Conn) { <-done; c.Close() }(raw)
			line.setConn(conn)
			if id := cid.id(); id != "" {
				_ = conn.Send(proto.Msg{Op: proto.OpAttach, PID: cmd.Process.Pid, Container: id})
			}
			say("re-attached %s as %s", o.Engine, lease)
		}
	}()

	err = cmd.Wait()
	close(done)
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil // exited cleanly; a grandchild still held the output copy
	}
	return exitCode(cmd, err)
}

// listen hears the daemon until the connection drops, passing each
// message to the runner's router, and returns why it dropped.
func listen(conn *proto.Conn, onMsg func(proto.Msg)) error {
	for {
		m, err := conn.Recv()
		if err != nil {
			return err
		}
		onMsg(m)
	}
}

// reattach dials berthd every Retry until one leases the running engine the
// card again, or the engine exits. A daemon that refuses (an older one that
// does not know reattach, or a PID that is no longer this engine) ends the
// attempts: the engine stays on record as unprotected until it exits.
func reattach(o Options, cmd *exec.Cmd, done <-chan struct{}, cid *containerRef) (*proto.Conn, net.Conn, string) {
	every := o.pollEvery()
	pid := cmd.Process.Pid
	start := procs.System{}.StartTime(pid)
	for {
		select {
		case <-done:
			return nil, nil, ""
		case <-time.After(every):
		}
		conn, raw, err := server.Dial(o.Socket)
		if err != nil {
			continue
		}
		err = conn.Send(proto.Msg{Op: proto.OpReattach, Engine: o.Engine, Group: o.Group, Priority: o.Priority, Needs: o.Needs, Pinned: o.Pinned, MinTurn: o.MinTurn.Seconds(), PID: pid, PIDStart: start, Container: cid.id()})
		var m proto.Msg
		if err == nil {
			m, err = conn.Recv()
		}
		switch {
		case err != nil:
			raw.Close()
		case m.Op == proto.OpGranted:
			return conn, raw, m.Lease
		default:
			raw.Close()
			fmt.Fprintf(o.Stderr, "berth: berthd would not re-attach %s (%s); it stays unprotected\n", o.Engine, m.Reason)
			<-done
			return nil, nil, ""
		}
	}
}

// jobBoundary waits for ComfyUI's queue to empty. It reports false when
// the engine exited meanwhile, true at the boundary. An unreadable queue
// (ComfyUI still starting, or wedged) is reported and waited out the same
// way: Berth has no proof a job is not running. A DrainMax ends the wait
// and the drain goes on to the stop ladder.
func jobBoundary(o Options, done <-chan struct{}, say func(string, ...any)) bool {
	every := o.pollEvery()
	last := ""
	wait := &boundaryWait{engine: o.Engine, max: o.DrainMax}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		running, pending, err := comfy.Queue(ctx, o.DrainComfyUI)
		cancel()
		var now string
		switch {
		case err != nil:
			now = fmt.Sprintf("cannot read the ComfyUI queue (%v); waiting", err)
			wait.note("queue unreadable", "queue unreadable")
		case running == 0 && pending == 0:
			say("ComfyUI queue empty; handing off")
			return true
		default:
			now = fmt.Sprintf("waiting for ComfyUI to finish: %d running, %d queued", running, pending)
			noun := "jobs"
			if running == 1 {
				noun = "job"
			}
			desc := fmt.Sprintf("%d %s running, %d queued", running, noun, pending)
			wait.note(desc, desc)
		}
		if now != last {
			say("%s", now)
			last = now
		}
		if wait.tick(say) {
			return true
		}
		select {
		case <-done:
			return false
		case <-time.After(every):
		}
	}
}

// drainProgressEvery is how often a drain boundary that is still busy
// says so, so the journal shows why the drain is taking.
var drainProgressEvery = 60 * time.Second

// boundaryWait tracks a drain boundary that has to wait (a processing
// slot, a running ComfyUI job, an unreadable queue): when the wait
// started and what its reads showed. It says so once a minute and ends
// the wait when a max has elapsed.
type boundaryWait struct {
	engine   string
	max      time.Duration // 0 = wait as long as it takes
	start    time.Time     // zero until the first note
	last     time.Time     // when the last progress line was printed
	progress string        // what the last read showed, in a progress line
	stop     string        // ...and in the line that ends the wait
}

// note remembers what the boundary shows and starts the wait clock on
// the first note.
func (w *boundaryWait) note(progress, stop string) {
	if w.start.IsZero() {
		w.start = time.Now()
		w.last = w.start
	}
	w.progress, w.stop = progress, stop
}

// tick says why the drain is still waiting, once a minute, and says the
// wait is over when the max has elapsed; it returns true then, and the
// drain continues as for an idle engine.
func (w *boundaryWait) tick(say func(string, ...any)) bool {
	if w.start.IsZero() {
		return false
	}
	if w.max > 0 && time.Since(w.start) >= w.max {
		say("%s still busy after %s (%s); draining anyway", w.engine, w.max, w.stop)
		return true
	}
	if time.Since(w.last) >= drainProgressEvery {
		say("draining %s: %s for %s", w.engine, w.progress, time.Since(w.start).Round(time.Second))
		w.last = time.Now()
	}
	return false
}

// slotSummary names how many slots a read showed busy, in the words a
// drain line uses: "1 slot busy", "3 slots processing".
func slotSummary(n int, state string) string {
	if n == 1 {
		return "1 slot " + state
	}
	return fmt.Sprintf("%d slots %s", n, state)
}

// slotBoundaryAt waits until the engine at base reports no processing
// slot, so an unload never cuts an in-flight request off. A host with no
// boundary at all (404, an unreachable host, a body that is not a slot
// list) unloads at once and says so. A sick boundary (any other
// status, a read timeout) waits like a busy one: Berth then has proof work
// was running and none that it stopped. An engine that exits meanwhile
// ends the drain. A DrainMax ends it too: after that long the drain goes
// on to the stop ladder, so a wedged engine that keeps reporting busy
// holds the card no longer.
func slotBoundaryAt(o Options, base string, done <-chan struct{}, say func(string, ...any)) bool {
	if base == "" {
		return true
	}
	every := o.pollEvery()
	url := base + "/slots"
	wait := &boundaryWait{engine: o.Engine, max: o.DrainMax}
	n, err := slotsBusy(url)
	if err != nil {
		if failOpen(err) {
			say("drain: no slot boundary at %s (%v); unloading anyway", url, err)
			return true
		}
		say("drain: slot boundary unreadable (%v); waiting for it to answer", err)
		wait.note("boundary unreadable", "boundary unreadable")
	} else if n == 0 {
		return true
	} else {
		say("waiting for the engine to finish its requests before unloading")
		wait.note(slotSummary(n, "busy"), slotSummary(n, "processing"))
	}
	for {
		select {
		case <-done:
			return false
		case <-time.After(every):
		}
		n, err := slotsBusy(url)
		switch {
		case err != nil:
			say("drain: lost the slot boundary (%v); waiting", err)
			wait.note("boundary unreadable", "boundary unreadable")
		case n == 0:
			say("engine idle; unloading")
			return true
		default:
			wait.note(slotSummary(n, "busy"), slotSummary(n, "processing"))
		}
		if wait.tick(say) {
			return true
		}
	}
}

// drainPostBase is the scheme://host the drain-post URL lives on, where its
// /slots boundary would answer; empty when the URL has no usable origin.
func drainPostBase(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// slotsErr carries enough of a failed /slots read for slotBoundary to
// tell "no boundary here" from "boundary sick". A status means the host
// answered; badBody means the body arrived but is not a slot list;
// timeout means the read hit its deadline without an answer.
type slotsErr struct {
	status  int // HTTP status, 0 when no response arrived
	badBody bool
	timeout bool
	err     error
}

func (e *slotsErr) Error() string { return e.err.Error() }

// failOpen reports whether a /slots failure means no boundary exists, so
// the drain may unload at once. 404, an unreachable host, or a body that
// is not a slot list fail open; any other status or a timeout waits.
func failOpen(err error) bool {
	var se *slotsErr
	if !errors.As(err, &se) {
		return false
	}
	if se.badBody {
		return true
	}
	if se.status == http.StatusNotFound {
		return true
	}
	if se.status != 0 {
		return false
	}
	return !se.timeout
}

func slotsTimeout(err error) bool {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded)
}

// slotsBusy counts the llama.cpp-style slots that are processing. The
// counting rule is engines.SlotBusy, the same one the engine reader uses.
// Failures come back as a *slotsErr so the caller can tell "no boundary"
// from "sick boundary" (see failOpen).
func slotsBusy(url string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, &slotsErr{err: err}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, &slotsErr{timeout: slotsTimeout(err), err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, &slotsErr{status: resp.StatusCode, err: fmt.Errorf("GET /slots: %s", resp.Status)}
	}
	var slots []struct {
		State        string `json:"state"`
		IsProcessing bool   `json:"is_processing"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&slots); err != nil {
		return 0, &slotsErr{badBody: true, err: fmt.Errorf("/slots: %w", err)}
	}
	n := 0
	for _, s := range slots {
		if engines.SlotBusy(s.State, s.IsProcessing) {
			n++
		}
	}
	return n, nil
}

func runUnleased(o Options, reason string) int {
	cmd := command(o.Argv, o.Output)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(o.Stderr, "berth: start %s: %v\n", o.Argv[0], err)
		return 127
	}
	afterStart(cmd)
	defer record(o, cmd, reason)()
	done := make(chan struct{})
	go forwardSignals(cmd, done)
	err := cmd.Wait()
	close(done)
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return exitCode(cmd, err)
}

// record leaves a note beside the socket that this engine runs without a
// lease, so berthd lists it instead of it being an anonymous squatter. It
// returns the func that removes the note.
func record(o Options, cmd *exec.Cmd, reason string) func() {
	pid := cmd.Process.Pid
	remove, err := runs.Write(runs.Dir(o.Socket), runs.Record{
		Engine: o.Engine, PID: pid, Start: procs.System{}.StartTime(pid), Started: time.Now(), Reason: reason,
	})
	if err != nil {
		fmt.Fprintf(o.Stderr, "berth: could not record the unprotected run: %v\n", err)
	}
	return remove
}

func command(argv []string, out io.Writer) *exec.Cmd {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if out != nil {
		cmd.Stdout, cmd.Stderr = io.MultiWriter(os.Stdout, out), io.MultiWriter(os.Stderr, out)
		// The copy runs through pipes. A grandchild that keeps them open
		// after the engine exits must not hold Wait, and with it the lease.
		cmd.WaitDelay = 2 * time.Second
	}
	isolate(cmd)
	return cmd
}

// forwardSignals passes stop signals (llama-swap stopping a model, Ctrl-C)
// to the engine's whole process group.
func forwardSignals(cmd *exec.Cmd, done <-chan struct{}) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, stopSignals...)
	defer signal.Stop(ch)
	for {
		select {
		case <-done:
			return
		case s := <-ch:
			signalGroup(cmd, s)
		}
	}
}

// drain stops the engine politely, then less so: the ComfyUI unload (models
// out of VRAM once an empty queue proves no job is running), the drain URL
// (llama-swap unloads the model and stops us itself, past its slot
// boundary), then a terminate to the process group, then a kill. Each step
// gets Grace to work.
func drain(o Options, cmd *exec.Cmd, line *leaseLine, done <-chan struct{}, reason string, say func(string, ...any)) {
	say("draining %s: %s", o.Engine, reason)
	if o.DrainComfyUI != "" {
		if !jobBoundary(o, done, say) {
			return
		}
		// A park hook does its own unload (with the RAM copy kept); only
		// an engine without one unloads-and-stops here.
		if o.Park == nil && !o.Hook {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := comfy.Free(ctx, o.DrainComfyUI, true)
			cancel()
			if err != nil {
				say("ComfyUI unload (%v); stopping the server instead", err)
			} else {
				say("ComfyUI models unloaded; stopping the server")
			}
		}
	}
	wait := func() bool {
		select {
		case <-done:
			return true
		case <-time.After(o.Grace):
			return false
		}
	}
	if o.DrainSlots != "" && o.DrainPost == "" {
		if !slotBoundaryAt(o, drainPostBase(o.DrainSlots), done, say) {
			return
		}
	} else if o.DrainPost != "" {
		if !slotBoundaryAt(o, drainPostBase(o.DrainPost), done, say) {
			return
		}
	}
	// The park step sits after the boundary, before anything stops the
	// engine: on admission the weights move to RAM, the lease is held
	// parked, and the stop ladder never runs.
	if o.Park != nil || o.Hook {
		if line.park(o, say) {
			return
		}
	}
	if o.DrainPost != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.DrainPost, strings.NewReader(""))
		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			say("drain url: %v", err)
		} else {
			resp.Body.Close()
			if wait() {
				return
			}
			say("drain url answered %s but %s is still running", resp.Status, o.Engine)
		}
	}
	if o.Container != "" && containerSupported() {
		// The engine lives in a container of ours: stop it through the
		// runtime CLI (its own SIGTERM, then SIGKILL inside the image)
		// instead of signalling the CLI wrapper's process group. The
		// target is the full ID the resolver answered, never the bare
		// name; before the name resolved there is no ID to stop, so the
		// group signals run as they always have.
		runtime, id := "", ""
		if o.container != nil {
			runtime, id = o.container.runtime(), o.container.id()
		}
		if id != "" {
			if runtime == "" {
				if rs := runtimes(); len(rs) > 0 {
					runtime = rs[0]
				}
			}
			argv := stopArgv(runtime, id, o.Grace)
			if _, err := o.CLI(argv, o.Grace+15*time.Second); err != nil {
				say("container stop failed (%v); falling back to the process group", err)
			} else {
				return
			}
		}
	}
	terminate(cmd)
	if wait() {
		return
	}
	say("%s ignored terminate for %s; killing", o.Engine, o.Grace)
	kill(cmd)
}
