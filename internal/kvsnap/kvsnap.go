// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package kvsnap saves a llama.cpp engine's KV caches and restores them,
// so a sequential run of sub-agents does not prefill its context again
// after every model switch.
//
// The flow has two halves. While Berth drains the holder, the engine's busy
// slots still hold a prompt: each one is saved through the engine's own slot
// action (the engine must start with --slot-save-path pointing at the
// snapshot directory), and the save is listed in the directory's ledger.
// When the next engine starts on the same model, its empty slot restores
// the newest snapshot for that model before the first request, so the
// prefill starts from the saved context.
//
// A snapshot is keyed by the engine and by the model identity the caller
// passes, the model path or id the engine reports. Snapshots never cross
// models: a restore only ever takes a file the ledger lists under the same
// key, and the ledger never guesses a key.
//
// The RAM tier for a snapshot is the kernel page cache. Warm asks the
// kernel to read the file back in, and the kernel keeps the pages only
// while the machine can spare the RAM. A warm file is cached, never
// reserved: Berth holds no RAM of its own, and a page the kernel gives back
// makes the restore read that byte from the SSD again.
//
// The package is a building block. The slot client speaks llama.cpp's
// /slots API; the index keeps the snapshot directory within a byte budget
// and above the storage floor. A later change wires it into the drain and
// start paths; nothing here touches a lease, a process or the GPU.
package kvsnap
