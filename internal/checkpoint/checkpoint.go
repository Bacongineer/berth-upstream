// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package checkpoint moves an unmodified CUDA process's device memory to
// host RAM and back with NVIDIA's cuda-checkpoint utility (driver 550 or
// later). Suspend locks the process's CUDA API, which waits for its GPU
// work to finish, then checkpoints it: the device memory lands in the
// process's own RAM and the card is free. Restore copies it back to the
// same addresses and unlocks. The process itself keeps running throughout;
// only its CUDA calls block while it is suspended.
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Tool runs cuda-checkpoint with argv and returns its combined output.
type Tool func(ctx context.Context, argv ...string) (string, error)

// Find is the installed cuda-checkpoint: on PATH, else beside the berth
// binary, since a service that runs berth (llama-swap's) often has a PATH
// without the user's own bin directory. The error says where to get it.
func Find() (Tool, error) {
	bin, err := exec.LookPath("cuda-checkpoint")
	if err != nil {
		if exe, xerr := os.Executable(); xerr == nil {
			beside := filepath.Join(filepath.Dir(exe), "cuda-checkpoint")
			if fi, serr := os.Stat(beside); serr == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
				bin, err = beside, nil
			}
		}
	}
	if err != nil {
		return nil, errors.New("cuda-checkpoint is not on PATH or beside the berth binary (github.com/NVIDIA/cuda-checkpoint, driver 550 or later)")
	}
	return func(ctx context.Context, argv ...string) (string, error) {
		out, err := exec.CommandContext(ctx, bin, argv...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}, nil
}

// Suspend locks pid's CUDA API, waiting up to lock for its GPU work to
// finish, then checkpoints its device memory into RAM. A checkpoint that
// fails unlocks again, so the process is never left locked.
func Suspend(ctx context.Context, run Tool, pid int, lock time.Duration) error {
	p := strconv.Itoa(pid)
	if out, err := run(ctx, "--action", "lock", "--pid", p, "--timeout", strconv.FormatInt(lock.Milliseconds(), 10)); err != nil {
		return fmt.Errorf("lock pid %d: %v: %s", pid, err, out)
	}
	if out, err := run(ctx, "--action", "checkpoint", "--pid", p); err != nil {
		if uout, uerr := run(ctx, "--action", "unlock", "--pid", p); uerr != nil {
			return fmt.Errorf("checkpoint pid %d: %v: %s; unlock failed too: %v: %s", pid, err, out, uerr, uout)
		}
		return fmt.Errorf("checkpoint pid %d: %v: %s", pid, err, out)
	}
	return nil
}

// Restore copies pid's device memory back onto the card and unlocks its
// CUDA API.
func Restore(ctx context.Context, run Tool, pid int) error {
	p := strconv.Itoa(pid)
	if out, err := run(ctx, "--action", "restore", "--pid", p); err != nil {
		return fmt.Errorf("restore pid %d: %v: %s", pid, err, out)
	}
	if out, err := run(ctx, "--action", "unlock", "--pid", p); err != nil {
		return fmt.Errorf("unlock pid %d: %v: %s", pid, err, out)
	}
	return nil
}
