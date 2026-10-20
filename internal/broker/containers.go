// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package broker

// Engines in a container. A containerized engine's GPU process is parented
// by containerd-shim, not by the docker CLI the wrapper started, so the
// process tree cannot reach it. The wrapper names the container instead
// (--container), the runtime resolves it to a full ID, and a GPU process
// whose cgroups carry that ID belongs to the lease.

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

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

// AttachContainer records the container whose processes hold this lease's
// memory: berth run resolved the operator's name to the full ID and named
// it. Only this lease's own container is ever attributed by it.
func (l *Lease) AttachContainer(id string) {
	if id == "" {
		return
	}
	l.b.mu.Lock()
	defer l.b.mu.Unlock()
	l.l.container = strings.ToLower(id)
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
