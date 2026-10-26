// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Engines in a container. A containerized engine's GPU process is parented
// by containerd-shim, not by the docker CLI the wrapper started, so the
// process tree cannot reach it. The wrapper names its containers instead
// (--container, or every container of a Compose project), the runtime
// resolves them to full IDs, and a GPU process whose cgroups carry one of
// those IDs belongs to the lease.

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// containerSet is the container set a client named: lowercase IDs, no
// repeats, nothing for a host engine.
func containerSet(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// containerOf is a process's container id (lowercase), "" for a host process.
func (b *Broker) containerOf(pid int) string {
	id, ok := b.cfg.Containers(pid)
	if !ok {
		return ""
	}
	return strings.ToLower(id)
}

// containerLabel resolves a container id's display name once per id, in the
// background (the resolution must not hold the broker lock), and stamps it
// on the squatter that carries the id. Called with b.mu held.
func (b *Broker) containerLabel(id string, set func(string)) {
	if n, ok := b.containerNames[id]; ok {
		set(n)
		return
	}
	if b.containerAsked[id] {
		return
	}
	b.containerAsked[id] = true
	go func() {
		n := b.cfg.ContainerName(id)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.containerNames[id] = n
		set(n)
	}()
}

// short is the 12 characters of a container id people quote.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// AttachContainers records the containers whose processes hold this lease's
// memory: berth run resolved the operator's names (or its Compose project)
// to full IDs and sends the whole set whenever it changes, so a container
// that restarted under a new ID is the lease's again on the next poll. Only
// these containers' processes are ever attributed to this lease.
func (l *Lease) AttachContainers(ids []string) {
	set := containerSet(ids)
	if len(set) == 0 {
		return
	}
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	l.l.containers = set
}

// hasContainer is whether a GPU process runs in one of this lease's
// containers. A host process, whose id is "", never matches. Called with
// b.mu held.
func (l *lease) hasContainer(id string) bool {
	return id != "" && slices.Contains(l.containers, id)
}

// wslVMWorker is the process WSL2 runs a Linux VM in, and the only GPU
// process Windows sees for a Docker Desktop container: the driver gives no
// per-container number there, and the counter Berth reads names this
// process, with the VM's whole dedicated usage. The match is a process name
// so the rule runs the same on every platform and the fake probe can test
// it. A GPU process in WSL outside Docker lands in the same row and is
// charged to the lease too, which counts too much and never too little.
const wslVMWorker = "vmwp.exe"

// soleContainerLease is the one lease holding the card that names
// containers, nil when none does or more than one does. Called with b.mu
// held.
func (b *Broker) soleContainerLease() *lease {
	var found *lease
	for _, l := range b.leases {
		if !l.ownsNew() || len(l.containers) == 0 {
			continue
		}
		if found != nil {
			return nil
		}
		found = l
	}
	return found
}

// wslVMLease is the lease a WSL VM's GPU row belongs to: the sole container
// lease on the card. Two of them share one row Berth cannot split, so it
// belongs to neither and stays a stowaway counted once against the card.
// Called with b.mu held.
func (b *Broker) wslVMLease(name string) *lease {
	if !strings.EqualFold(name, wslVMWorker) {
		return nil
	}
	return b.soleContainerLease()
}

// containerNameCLI resolves a container id to its runtime name with the
// runtime CLI: docker, then podman. "" when neither answers. Best effort:
// the report shows the short ID either way. Each ask runs under a 5 s
// deadline, so a hung runtime daemon leaks no goroutine.
func containerNameCLI(id string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err != nil {
			continue
		}
		out, err := exec.CommandContext(ctx, rt, "inspect", "--format", "{{.Name}}", id).Output()
		if err != nil {
			continue
		}
		return strings.TrimPrefix(strings.TrimSpace(string(out)), "/")
	}
	return ""
}
