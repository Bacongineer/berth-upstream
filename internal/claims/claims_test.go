package claims

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/broker"
	"github.com/T-Crypt/berth/internal/proto"
)

func compiled(t *testing.T, rs ...Rule) []Rule {
	t.Helper()
	for i := range rs {
		if err := rs[i].Compile(); err != nil {
			t.Fatal(err)
		}
	}
	return rs
}

// A Steam session: steam -> reaper -> proton -> the game. Only the game side
// of reaper is the game; steam and reaper themselves are not.
var steam = []Proc{
	{PID: 1, Name: "systemd"},
	{PID: 10, Name: "steam", Parent: 1},
	{PID: 20, Name: "reaper", Parent: 10},
	{PID: 30, Name: "proton", Parent: 20},
	{PID: 40, Name: "eldenring.exe", Argv0: "eldenring.exe", Parent: 30, Cmdline: `Z:\game\eldenring.exe`},
	{PID: 50, Name: "blender", Argv0: "blender", Parent: 1},
	{PID: 60, Name: "python3", Argv0: "python3", Parent: 1, Cmdline: "python3 train.py --epochs 3"},
}

func TestBuiltinMatches(t *testing.T) {
	m := Match(compiled(t, Builtin()...), steam)
	if got := pids(m["steam-game"]); len(got) != 2 || got[0] != 30 || got[1] != 40 {
		t.Fatalf("steam-game = %v, want [30 40]", got)
	}
	if got := pids(m["blender"]); len(got) != 1 || got[0] != 50 {
		t.Fatalf("blender = %v", got)
	}
	if len(m) != 2 {
		t.Fatalf("unexpected matches: %v", m)
	}
}

func TestCmdlineAndFirstRuleWins(t *testing.T) {
	rs := compiled(t,
		Rule{Name: "training", Cmdline: `train\.py`, Action: ActionHold},
		Rule{Name: "python", Exe: []string{"PYTHON*"}, Action: ActionTrack},
	)
	m := Match(rs, steam)
	if len(m["training"]) != 1 || len(m["python"]) != 0 {
		t.Fatalf("first matching rule must own the process: %v", m)
	}
}

func TestCompileRejects(t *testing.T) {
	for _, r := range []Rule{
		{},
		{Name: "x"},
		{Name: "x", Exe: []string{"a"}, Action: "kill"},
		{Name: "x", Cmdline: "("},
		{Name: "x", Exe: []string{"["}},
	} {
		if err := r.Compile(); err == nil {
			t.Errorf("%+v compiled", r)
		}
	}
}

type fakeBroker struct {
	mu      sync.Mutex
	tracked map[int]string
	states  []proto.Claim
	asked   []broker.Request
	events  []string
	held    chan struct{}
}

func (f *fakeBroker) Acquire(ctx context.Context, r broker.Request, _ func(string)) (*broker.Lease, error) {
	f.mu.Lock()
	f.asked = append(f.asked, r)
	f.mu.Unlock()
	f.held <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (f *fakeBroker) SetClaims(t map[int]string, s []proto.Claim) {
	f.mu.Lock()
	f.tracked, f.states = t, s
	f.mu.Unlock()
}
func (f *fakeBroker) Note(e proto.Event) {
	f.mu.Lock()
	f.events = append(f.events, e.Kind)
	f.mu.Unlock()
}

// A hold starts on the first match, survives a gap shorter than the linger,
// and ends once the game has been gone for the linger.
func TestWatcherHoldLifecycle(t *testing.T) {
	procs := steam
	fb := &fakeBroker{held: make(chan struct{}, 4)}
	w := &Watcher{Rules: Builtin(), B: fb, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Scan: func() ([]Proc, error) { return procs, nil }, holds: map[string]*hold{}}
	w.Rules = compiled(t, w.Rules...)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t0 := time.Now()

	w.Tick(ctx, t0)
	select {
	case <-fb.held:
	case <-time.After(time.Second):
		t.Fatal("no hold requested")
	}
	if r := fb.asked[0]; r.Priority != broker.Hold || !r.Pinned || r.Engine != "claim:steam-game" {
		t.Fatalf("request = %+v", r)
	}
	if len(fb.tracked) != 1 || fb.tracked[50] != "blender" {
		t.Fatalf("tracked = %v", fb.tracked)
	}

	procs = steam[:3] // the game exits
	w.Tick(ctx, t0.Add(5*time.Second))
	if w.holds["steam-game"] == nil {
		t.Fatal("hold ended inside the linger")
	}
	w.Tick(ctx, t0.Add(DefaultLinger+time.Second))
	if w.holds["steam-game"] != nil {
		t.Fatal("hold outlived the linger")
	}
	if got := strings.Join(fb.events, ","); got != "claim.started,claim.ended" {
		t.Fatalf("events = %s", got)
	}
	if len(fb.asked) != 1 {
		t.Fatalf("hold requested %d times", len(fb.asked))
	}
}
