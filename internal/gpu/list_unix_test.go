// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package gpu

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// --gpu all opens what List finds: every NVIDIA card from one query.
func TestListEveryNvidiaCard(t *testing.T) {
	bin, _ := fakeTwoCards(t)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	devs, err := List(context.Background(), Options{Backend: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || devs[0].Index != 0 || devs[1].Index != 1 || devs[1].Name != "RTX 6000" {
		t.Fatalf("devices = %+v", devs)
	}
}

// UUID reads one card's UUID by its PCI address.
func TestUUIDByAddress(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n*00000000:02:00.0*uuid*|*uuid*00000000:02:00.0*) echo GPU-7f3c2a10-aaaa-bbbb-cccc-000000000002 ;;\n*) echo 'No devices were found' >&2; exit 6 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	got, err := UUID(context.Background(), "00000000:02:00.0")
	if err != nil || got != "GPU-7f3c2a10-aaaa-bbbb-cccc-000000000002" {
		t.Fatalf("UUID = %q, %v", got, err)
	}
	if got, err := UUID(context.Background(), "00000000:03:00.0"); err == nil {
		t.Fatalf("a missing card gave %q", got)
	}
}
