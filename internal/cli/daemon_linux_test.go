// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/T-Crypt/berth/internal/gpu"
)

type goneProbe struct{}

func (goneProbe) Read(context.Context) (gpu.Snapshot, error) {
	return gpu.Snapshot{}, errors.New("no GPU at 0000:02:00.0 (found 1)")
}

// berthd starts while the card it names by address is in a VM, and still
// refuses a card that is missing for any other reason.
func TestInventoryStartsWithItsCardInAVM(t *testing.T) {
	root := t.TempDir()
	gpu.SysfsRoot = root
	t.Cleanup(func() { gpu.SysfsRoot = "/sys" })
	for addr, driver := range map[string]string{"0000:01:00.0": "nvidia", "0000:02:00.0": "vfio-pci"} {
		dev := filepath.Join(root, "bus", "pci", "devices", addr)
		drv := filepath.Join(root, "bus", "pci", "drivers", driver)
		for _, d := range []string{dev, drv} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(drv, filepath.Join(dev, "driver")); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, inVM, err := inventory(context.Background(), goneProbe{}, "0000:02:00.0", log); err != nil || !inVM {
		t.Fatalf("card in a VM: in VM %v, %v, want berthd to start", inVM, err)
	}
	for _, addr := range []string{"", "0000:01:00.0", "0000:03:00.0"} {
		if _, _, err := inventory(context.Background(), goneProbe{}, addr, log); err == nil {
			t.Fatalf("addr %q: started without its card", addr)
		}
	}
}
