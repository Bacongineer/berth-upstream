// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/gpu"
	"github.com/T-Crypt/berth/internal/procs"
	"github.com/T-Crypt/berth/internal/proto"
)

// vfioBroker is a broker over the fake probe, configured the way the hold
// tests set theirs up.
func vfioBroker(t *testing.T, p gpu.Probe) *Broker {
	t.Helper()
	return New(Config{
		ReleaseGrace: time.Hour,
		DefaultWait:  2 * time.Second,
		JuggleWindow: -1,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, p, procs.Map{})
}

// vfioRequest is the acquire V2's command makes, pinned at hold priority
// in the group the broker matches on.
func vfioRequest() Request {
	return Request{Engine: "vfio:galaxy", Group: "vfio", Priority: Hold, Pinned: true}
}

// fakeProbe is a card reader that counts its calls and fails on demand,
// so a test can watch whether the broker opens the device at all.
type fakeProbe struct {
	mu    sync.Mutex
	calls int
	fail  bool
	busID string
}

func (f *fakeProbe) set(fail bool, busID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail, f.busID = fail, busID
}

func (f *fakeProbe) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeProbe) Read(context.Context) (gpu.Snapshot, error) {
	f.mu.Lock()
	f.calls++
	fail, busID := f.fail, f.busID
	f.mu.Unlock()
	if fail {
		return gpu.Snapshot{}, errors.New("device gone")
	}
	return gpu.Snapshot{
		Index: 0, Name: "RTX 4090", Total: 24 * GiB,
		Devices: []gpu.Device{{Index: 0, Name: "RTX 4090", BusID: busID, Total: 24 * GiB}},
	}, nil
}

// sysfsCard builds a fake sysfs root with the PCI function at addr bound
// to driver ("" unbound), and points gpu.SysfsRoot at it for the test. The
// root is returned for later rebinds.
func sysfsCard(t *testing.T, addr, driver string) string {
	t.Helper()
	root := t.TempDir()
	bindCard(t, root, addr, driver)
	gpu.SysfsRoot = root
	t.Cleanup(func() { gpu.SysfsRoot = "/sys" })
	return root
}

// bindCard points the function's driver symlink at driver, "" unbinding it.
func bindCard(t *testing.T, root, addr, driver string) {
	t.Helper()
	base := filepath.Join(root, "bus", "pci", "devices", addr)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(base, "driver")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if driver == "" {
		return
	}
	dst := filepath.Join(root, "bus", "pci", "drivers", driver)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dst, filepath.Join(base, "driver")); err != nil {
		t.Fatal(err)
	}
}

// displayAttrs writes the vendor and class a discovery scan reads.
func displayAttrs(t *testing.T, root, addr string) {
	t.Helper()
	base := filepath.Join(root, "bus", "pci", "devices", addr)
	for name, value := range map[string]string{"vendor": "0x10de", "class": "0x030000"} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// skipNonLinux guards the behavior tests: the card state reads sysfs,
// which only Linux has.
func skipNonLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the card state reads sysfs")
	}
}

// kinds counts the broker's events by kind.
func kinds(b *Broker) map[string]int {
	m := map[string]int{}
	evs, _, stop := b.Events(0, false)
	defer stop()
	for _, e := range evs {
		m[e.Kind]++
	}
	return m
}

// lastWaiting is the newest lease.waiting reason.
func lastWaiting(b *Broker) string {
	why := ""
	evs, _, stop := b.Events(0, false)
	defer stop()
	for _, e := range evs {
		if e.Kind == proto.EvWaiting {
			why = e.Detail
		}
	}
	return why
}

// While a VFIO hold is granted, Tick never calls the probe.
func TestVFIOHoldSkipsProbe(t *testing.T) {
	skipNonLinux(t)
	sysfsCard(t, "0000:01:00.0", "nvidia")
	p := &fakeProbe{busID: "00000000:01:00.0"}
	b := vfioBroker(t, p)
	b.Tick(context.Background())
	hold := must(t, acquire(b, vfioRequest()))
	defer hold.Release()

	b.Tick(context.Background())
	b.Tick(context.Background())

	if n := p.reads(); n != 1 {
		t.Fatalf("probe ran %d times, want 1 (the tick before the hold)", n)
	}
	if card := b.Status().Passthrough; card == nil || card.State != proto.CardDetaching || card.Driver != "nvidia" {
		t.Fatalf("card state = %+v, want detaching on driver nvidia", card)
	}
	st := b.Status()
	if len(st.Leases) != 1 || st.Leases[0].State != "active" || st.Leases[0].Group != "vfio" {
		t.Fatalf("leases = %+v, want the VFIO hold HOT", st.Leases)
	}
}

// Detaching, then passthrough, then returned: one event per transition and
// no probe-failed warning anywhere.
func TestVFIOTransitions(t *testing.T) {
	skipNonLinux(t)
	root := sysfsCard(t, "0000:01:00.0", "nvidia")
	p := &fakeProbe{busID: "00000000:01:00.0"}
	b := vfioBroker(t, p)
	b.Tick(context.Background())
	hold := must(t, acquire(b, vfioRequest()))

	b.Tick(context.Background())
	if card := b.Status().Passthrough; card == nil || card.State != proto.CardDetaching {
		t.Fatalf("card state = %+v, want detaching while the hold is on", card)
	}

	bindCard(t, root, "0000:01:00.0", "vfio-pci")
	b.Tick(context.Background())
	if card := b.Status().Passthrough; card == nil || card.State != proto.CardPassthrough || card.Driver != "vfio-pci" {
		t.Fatalf("card state = %+v, want passthrough on vfio-pci", card)
	}
	if n := p.reads(); n != 1 {
		t.Fatalf("probe ran %d times under the hold, want 1", n)
	}

	hold.Release()
	bindCard(t, root, "0000:01:00.0", "nvidia")
	b.Tick(context.Background())
	if card := b.Status().Passthrough; card != nil {
		t.Fatalf("card state = %+v, want none once the probe answers again", card)
	}

	k := kinds(b)
	if k[proto.EvCardPassthrough] != 1 || k[proto.EvCardReturned] != 1 {
		t.Fatalf("events = %v, want one passthrough and one returned", k)
	}
	if k[proto.EvProbeFailed] != 0 {
		t.Fatalf("probe.failed fired %d times, want 0", k[proto.EvProbeFailed])
	}
}

// A waiter under passthrough is told why in plain words and is not granted.
func TestWaiterToldTheCardIsPassthrough(t *testing.T) {
	skipNonLinux(t)
	sysfsCard(t, "0000:01:00.0", "vfio-pci")
	p := &fakeProbe{busID: "00000000:01:00.0"}
	b := vfioBroker(t, p)
	b.Tick(context.Background())
	hold := must(t, acquire(b, vfioRequest()))
	defer hold.Release()
	b.Tick(context.Background())
	if card := b.Status().Passthrough; card == nil || card.State != proto.CardPassthrough {
		t.Fatalf("card state = %+v, want passthrough", card)
	}

	waiter := acquire(b, Request{Engine: "llama", Priority: Foreground, Needs: 4 * GiB})
	pending(t, waiter)
	b.Tick(context.Background())

	st := b.Status()
	if len(st.Leases) != 1 {
		t.Fatalf("granted under passthrough: %+v", st.Leases)
	}
	if len(st.Waiting) != 1 || st.Waiting[0].Engine != "llama" || st.Waiting[0].Position != 1 {
		t.Fatalf("waiting = %+v, want llama next in line", st.Waiting)
	}
	select {
	case <-waiter:
		t.Fatal("the waiter was granted a card that is in a VM")
	default:
	}
	why := lastWaiting(b)
	if !strings.Contains(why, "next in line: gpu0 is passed through to a VM (vfio-pci); it comes back when the VM's hold ends") {
		t.Fatalf("wait reason = %q", why)
	}
}

// A probe failure with the card bound to vfio-pci and no hold is a hand
// switch: the card reads passthrough, the probe stays silent, and the card
// is reported returned once the probe answers again.
func TestHandSwitchedPassthrough(t *testing.T) {
	skipNonLinux(t)
	root := sysfsCard(t, "0000:01:00.0", "vfio-pci")
	displayAttrs(t, root, "0000:01:00.0")
	p := &fakeProbe{}
	p.set(true, "")
	b := vfioBroker(t, p)
	b.Tick(context.Background())
	st := b.Status()
	if card := st.Passthrough; card == nil || card.State != proto.CardPassthrough || card.Driver != "vfio-pci" {
		t.Fatalf("card state = %+v, want passthrough on vfio-pci", card)
	}
	if st.ProbeErr != "" {
		t.Fatalf("probe error = %q, want none in passthrough", st.ProbeErr)
	}

	p.set(false, "00000000:01:00.0")
	b.Tick(context.Background())
	if card := b.Status().Passthrough; card != nil {
		t.Fatalf("card state = %+v, want none after the card returns", card)
	}
	k := kinds(b)
	if k[proto.EvCardPassthrough] != 1 || k[proto.EvCardReturned] != 1 || k[proto.EvProbeFailed] != 0 {
		t.Fatalf("events = %v, want one passthrough, one returned, no probe failure", k)
	}
}

// Without exactly one vfio-bound card to find, a probe failure keeps
// today's warning.
func TestProbeFailureWithoutOneVFIOCardWarns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding []string // one driver name per card, in address order
	}{
		{"no display function bound", nil},
		{"card on its host driver", []string{"nvidia"}},
		{"two vfio cards", []string{"vfio-pci", "vfio-pci"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skipNonLinux(t)
			root := t.TempDir()
			gpu.SysfsRoot = root
			t.Cleanup(func() { gpu.SysfsRoot = "/sys" })
			for i, addr := range []string{"0000:01:00.0", "0000:02:00.0"} {
				driver := ""
				if i < len(tc.binding) {
					driver = tc.binding[i]
				}
				if driver != "" {
					bindCard(t, root, addr, driver)
					displayAttrs(t, root, addr)
				}
			}
			p := &fakeProbe{}
			p.set(true, "")
			b := vfioBroker(t, p)
			b.Tick(context.Background())

			if card := b.Status().Passthrough; card != nil {
				t.Fatalf("card state = %+v, want none", card)
			}
			if k := kinds(b); k[proto.EvProbeFailed] != 1 {
				t.Fatalf("probe.failed fired %d times, want today's warning once", k[proto.EvProbeFailed])
			}
		})
	}
}

// A card remembered on nvidia whose probe then fails is an ordinary probe
// failure: the warning stands.
func TestProbeFailureOnNvidiaWarns(t *testing.T) {
	skipNonLinux(t)
	root := sysfsCard(t, "0000:01:00.0", "nvidia")
	p := &fakeProbe{}
	p.set(false, "00000000:01:00.0")
	b := vfioBroker(t, p)
	b.Tick(context.Background())
	p.set(true, "")
	b.Tick(context.Background())

	if card := b.Status().Passthrough; card != nil {
		t.Fatalf("card state = %+v, want none", card)
	}
	if k := kinds(b); k[proto.EvProbeFailed] != 1 {
		t.Fatalf("probe.failed fired %d times, want once", k[proto.EvProbeFailed])
	}
	_ = root
}
