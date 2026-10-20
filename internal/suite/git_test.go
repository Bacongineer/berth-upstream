// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"fmt"
	"strings"
	"testing"
)

// fakeGit is a Git over in-memory per-dir state: the current sha and branch,
// a dirty flag, the origin URL, a ref table, and a record of every call, so
// a test can assert both the outcome and the order of the git operations.
type fakeGit struct {
	heads     map[string]string // dir -> current sha
	branches  map[string]string // dir -> branch; "" = detached
	dirty     map[string]bool
	remotes   map[string]string            // dir -> origin URL; "" = no origin
	refs      map[string]map[string]string // dir -> ref -> sha
	checkouts []string                     // recorded "dir sha" pairs
	calls     []string
}

func newFakeGit() *fakeGit {
	return &fakeGit{
		heads:    map[string]string{},
		branches: map[string]string{},
		dirty:    map[string]bool{},
		remotes:  map[string]string{},
		refs:     map[string]map[string]string{},
	}
}

func (f *fakeGit) note(dir string, args ...string) {
	f.calls = append(f.calls, dir+": "+strings.Join(args, " "))
}

func (f *fakeGit) RemoteURL(dir string) (string, error) {
	f.note(dir, "remote")
	url, ok := f.remotes[dir]
	if !ok {
		return "", nil
	}
	return url, nil
}

func (f *fakeGit) Head(dir string) (string, error) {
	f.note(dir, "rev-parse", "HEAD")
	sha, ok := f.heads[dir]
	if !ok {
		return "", fmt.Errorf("%s: not a checkout", dir)
	}
	return sha, nil
}

func (f *fakeGit) Branch(dir string) (string, error) {
	f.note(dir, "symbolic-ref")
	return f.branches[dir], nil
}

func (f *fakeGit) Dirty(dir string) (bool, error) {
	f.note(dir, "status")
	return f.dirty[dir], nil
}

func (f *fakeGit) Fetch(dir string) error {
	f.note(dir, "fetch")
	if _, ok := f.heads[dir]; !ok {
		return fmt.Errorf("%s: not a checkout", dir)
	}
	return nil
}

func (f *fakeGit) Ref(dir, ref string) (string, error) {
	f.note(dir, "rev-parse", ref)
	sha, ok := f.refs[dir][ref]
	if !ok {
		return "", fmt.Errorf("%s: unknown ref %s", dir, ref)
	}
	return sha, nil
}

func (f *fakeGit) Checkout(dir, sha string) error {
	f.note(dir, "checkout", sha)
	f.heads[dir] = sha
	f.checkouts = append(f.checkouts, dir+" "+sha)
	return nil
}

func (f *fakeGit) set(dir, sha, branch, remote string, refs map[string]string) {
	f.heads[dir] = sha
	f.branches[dir] = branch
	if remote != "" {
		f.remotes[dir] = remote
	}
	f.refs[dir] = refs
}

const (
	shaA = "aaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccc"
)

func TestUpdatePinsNewShaAndSnapshotsTheOld(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaA, "master", "https://github.com/comfyanonymous/ComfyUI.git",
		map[string]string{"origin/master": shaB})
	comp := &Component{Name: "comfyui", Channel: "master", Commit: shaA}
	if err := Update(g, "/x", comp, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if comp.Commit != shaB || comp.Previous != shaA {
		t.Errorf("component = commit %s previous %s, want %s / %s", comp.Commit, comp.Previous, shaB, shaA)
	}
	if g.heads["/x"] != shaB {
		t.Errorf("checkout left HEAD at %s, want %s", g.heads["/x"], shaB)
	}
	// the fetch happens before the checkout: the snapshot is of what the
	// remote actually had, not what was last fetched
	wantFetch, wantCheckout := "/x: fetch", "/x: checkout "+shaB
	first, last := -1, -1
	for i, c := range g.calls {
		if c == wantFetch {
			first = i
		}
		if c == wantCheckout {
			last = i
		}
	}
	if first < 0 || last < 0 || first > last {
		t.Errorf("calls = %v, want fetch before %s", g.calls, wantCheckout)
	}
}

func TestUpdateAgainMovesTheSnapshotForward(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaB, "master", "", map[string]string{"origin/master": shaC})
	comp := &Component{Name: "comfyui", Channel: "master", Commit: shaB, Previous: shaA}
	if err := Update(g, "/x", comp, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if comp.Commit != shaC || comp.Previous != shaB {
		t.Errorf("component = commit %s previous %s, want %s / %s", comp.Commit, comp.Previous, shaC, shaB)
	}
}

func TestUpdateAtTheTipIsANoOp(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaB, "master", "", map[string]string{"origin/master": shaB})
	comp := &Component{Name: "comfyui", Channel: "master", Commit: shaB, Previous: shaA}
	if err := Update(g, "/x", comp, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	if comp.Commit != shaB || comp.Previous != shaA {
		t.Errorf("component moved at the tip: %s / %s", comp.Commit, comp.Previous)
	}
	if len(g.checkouts) != 0 {
		t.Errorf("checkouts = %v, want none", g.checkouts)
	}
}

func TestUpdateChannelOverrideBeatsTheRecordedChannel(t *testing.T) {
	g := newFakeGit()
	// a detached checkout: the recorded channel is empty, the operator names one
	g.set("/x", shaA, "", "", map[string]string{"origin/nightly": shaB})
	comp := &Component{Name: "comfyui", Channel: "", Commit: shaA}
	if err := Update(g, "/x", comp, "nightly"); err != nil {
		t.Fatalf("update: %v", err)
	}
	if comp.Commit != shaB {
		t.Errorf("commit = %s, want %s", comp.Commit, shaB)
	}
}

func TestRollbackSwapsThePinsBack(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaB, "master", "", map[string]string{})
	comp := &Component{Name: "comfyui", Commit: shaB, Previous: shaA}
	if err := Rollback(g, "/x", comp); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if comp.Commit != shaA || comp.Previous != shaB {
		t.Errorf("component = commit %s previous %s, want %s / %s", comp.Commit, comp.Previous, shaA, shaB)
	}
	if g.heads["/x"] != shaA {
		t.Errorf("HEAD = %s, want %s", g.heads["/x"], shaA)
	}
	// a second rollback flips back to the newer pin
	if err := Rollback(g, "/x", comp); err != nil {
		t.Fatalf("second rollback: %v", err)
	}
	if comp.Commit != shaB || comp.Previous != shaA {
		t.Errorf("second rollback: %s / %s", comp.Commit, comp.Previous)
	}
}

func TestRollbackWithoutAPreviousCommitFails(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaA, "master", "", map[string]string{})
	comp := &Component{Name: "comfyui", Commit: shaA}
	if err := Rollback(g, "/x", comp); err == nil || !strings.Contains(err.Error(), "no previous commit") {
		t.Errorf("error = %v, want it to name the missing previous commit", err)
	}
	if comp.Commit != shaA {
		t.Errorf("commit moved: %s", comp.Commit)
	}
}

func TestUpdateRefusesADirtyComponent(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaA, "master", "", map[string]string{"origin/master": shaB})
	g.dirty["/x"] = true
	comp := &Component{Name: "comfyui", Channel: "master", Commit: shaA, Dirty: true}
	err := Update(g, "/x", comp, "")
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Errorf("error = %v, want a refusal naming the dirty tree", err)
	}
	if len(g.calls) != 0 {
		t.Errorf("git was touched on a dirty tree: %v", g.calls)
	}
}

func TestRollbackRefusesADirtyComponent(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaB, "master", "", map[string]string{})
	g.dirty["/x"] = true
	comp := &Component{Name: "comfyui", Commit: shaB, Previous: shaA, Dirty: true}
	err := Rollback(g, "/x", comp)
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Errorf("error = %v, want a refusal naming the dirty tree", err)
	}
	if len(g.checkouts) != 0 {
		t.Errorf("checkout on a dirty tree: %v", g.checkouts)
	}
}

func TestUpdateWithNoChannelAtAllFails(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaA, "", "", map[string]string{})
	comp := &Component{Name: "comfyui", Commit: shaA}
	err := Update(g, "/x", comp, "")
	if err == nil || !strings.Contains(err.Error(), "--channel") {
		t.Errorf("error = %v, want it to name --channel", err)
	}
}

func TestRefreshDirtyRereadsTheTree(t *testing.T) {
	g := newFakeGit()
	g.set("/x", shaA, "master", "", map[string]string{})
	comp := &Component{Name: "comfyui", Commit: shaA}
	if err := RefreshDirty(g, "/x", comp); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if comp.Dirty {
		t.Error("clean tree read as dirty")
	}
	g.dirty["/x"] = true
	if err := RefreshDirty(g, "/x", comp); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if !comp.Dirty {
		t.Error("dirty tree read as clean")
	}
}
