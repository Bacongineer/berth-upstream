// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package suite

import (
	"fmt"
	"os/exec"
	"strings"
)

// Git is the seam over the system git binary: every component operation is
// these few verbs, so a test substitutes a fake and needs no git or network.
type Git interface {
	RemoteURL(dir string) (string, error) // the origin remote's URL; "" when the checkout has no origin
	Head(dir string) (string, error)      // the current sha (works on a branch and detached)
	Branch(dir string) (string, error)    // the current branch; "" when detached
	Dirty(dir string) (bool, error)       // the working tree has uncommitted local changes
	Fetch(dir string) error               // fetch --all --prune
	Ref(dir, ref string) (string, error)  // rev-parse a ref such as origin/master
	Checkout(dir, sha string) error       // detached checkout of sha
}

// NewGit returns the implementation over the system git binary.
func NewGit() Git { return realGit{} }

type realGit struct{}

// runGit runs git -C dir args and returns its trimmed stdout.
func runGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (realGit) RemoteURL(dir string) (string, error) {
	list, err := runGit(dir, "remote")
	if err != nil {
		return "", err
	}
	hasOrigin := false
	for _, r := range strings.Fields(list) {
		if r == "origin" {
			hasOrigin = true
		}
	}
	if !hasOrigin {
		return "", nil
	}
	return runGit(dir, "remote", "get-url", "origin")
}

func (realGit) Head(dir string) (string, error) { return runGit(dir, "rev-parse", "HEAD") }

func (realGit) Branch(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "-q", "HEAD").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			if ee.ExitCode() == 1 {
				return "", nil // detached: symbolic-ref exits 1 with empty output
			}
			return "", fmt.Errorf("git symbolic-ref: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git symbolic-ref: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (realGit) Dirty(dir string) (bool, error) {
	out, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func (realGit) Fetch(dir string) error {
	_, err := runGit(dir, "fetch", "--all", "--prune")
	return err
}

func (realGit) Ref(dir, ref string) (string, error) { return runGit(dir, "rev-parse", ref) }

func (realGit) Checkout(dir, sha string) error {
	_, err := runGit(dir, "checkout", "--detach", sha)
	return err
}

// Update moves the component to the tip of its channel: the channel override
// beats the component's own channel (an adopted checkout may be detached, so
// its recorded channel is empty), a new sha becomes the pin and the old pin
// the rollback snapshot. A component with local changes is refused. A
// component already at the tip is left exactly as it is, snapshot included.
func Update(g Git, dir string, comp *Component, channelOverride string) error {
	if comp.Dirty {
		return fmt.Errorf("%s has local changes (dirty); commit or revert them before an update", comp.Name)
	}
	channel := channelOverride
	if channel == "" {
		channel = comp.Channel
	}
	if channel == "" {
		return fmt.Errorf("%s: the component has no channel (detached); pass --channel REF", comp.Name)
	}
	if err := g.Fetch(dir); err != nil {
		return err
	}
	target, err := g.Ref(dir, "origin/"+channel)
	if err != nil {
		return fmt.Errorf("%s: %w", comp.Name, err)
	}
	if target == comp.Commit {
		return nil
	}
	if err := g.Checkout(dir, target); err != nil {
		return err
	}
	comp.Previous = comp.Commit
	comp.Commit = target
	return nil
}

// Rollback swaps the pin with the component's previous commit and checks out
// the restored sha. A second rollback flips back to the newer pin; a
// following update replaces the snapshot again. A component without a
// previous commit, or with local changes, is refused.
func Rollback(g Git, dir string, comp *Component) error {
	if comp.Previous == "" {
		return fmt.Errorf("%s: no previous commit to roll back to", comp.Name)
	}
	if comp.Dirty {
		return fmt.Errorf("%s has local changes (dirty); commit or revert them before a rollback", comp.Name)
	}
	restored := comp.Previous
	if err := g.Checkout(dir, restored); err != nil {
		return err
	}
	comp.Previous = comp.Commit
	comp.Commit = restored
	return nil
}

// RefreshDirty re-reads the component's dirty flag from the working tree, so
// a command acts on the tree as it is now, not as the state file recorded it.
func RefreshDirty(g Git, dir string, comp *Component) error {
	d, err := g.Dirty(dir)
	if err != nil {
		return err
	}
	comp.Dirty = d
	return nil
}
