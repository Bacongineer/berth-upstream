// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gpu

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setSysfs points SysfsRoot at a temp root and restores the old value.
func setSysfs(t *testing.T, root string) {
	t.Helper()
	old := SysfsRoot
	SysfsRoot = root
	t.Cleanup(func() { SysfsRoot = old })
}

// pciFunction builds one PCI function in the sysfs root: an address dir, a
// driver dir whose symlink binds it (driver "" leaves it unbound), and the
// given vendor and class attributes when the strings are set.
func pciFunction(t *testing.T, root, addr, driver, vendor, class string) {
	t.Helper()
	base := filepath.Join(root, "bus", "pci", "devices", addr)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if driver != "" {
		dst := filepath.Join(root, "bus", "pci", "drivers", driver)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dst, filepath.Join(base, "driver")); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{"vendor": vendor, "class": class} {
		if value == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(base, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSysfsAddr(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"00000000:01:00.0", "0000:01:00.0"},
		{"00000000:1A:00.0", "0000:1a:00.0"},
		{"0000C101:01:00.0", "c101:01:00.0"},
		{"", ""},
		{"01:00.0", ""},
		{"domain/01:00.0", ""},
	} {
		if got := SysfsAddr(tc.in); got != tc.want {
			t.Errorf("SysfsAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDriverFor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sysfs is a Linux view")
	}
	root := t.TempDir()
	setSysfs(t, root)
	pciFunction(t, root, "0000:01:00.0", "vfio-pci", "", "")
	pciFunction(t, root, "0000:02:00.0", "nvidia", "", "")
	pciFunction(t, root, "0000:03:00.0", "", "", "")

	if got := DriverFor("0000:01:00.0"); got != "vfio-pci" {
		t.Errorf("bound function: DriverFor = %q, want vfio-pci", got)
	}
	if got := DriverFor("0000:02:00.0"); got != "nvidia" {
		t.Errorf("host driver: DriverFor = %q, want nvidia", got)
	}
	if got := DriverFor("0000:03:00.0"); got != "" {
		t.Errorf("unbound function: DriverFor = %q, want empty", got)
	}
	if got := DriverFor("0000:04:00.0"); got != "" {
		t.Errorf("missing function: DriverFor = %q, want empty", got)
	}
	if got := DriverFor(""); got != "" {
		t.Errorf("empty address: DriverFor = %q, want empty", got)
	}
}

func TestFindVFIOCard(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("sysfs is a Linux view")
	}
	t.Run("one match", func(t *testing.T) {
		root := t.TempDir()
		setSysfs(t, root)
		pciFunction(t, root, "0000:01:00.0", "vfio-pci", "0x10de", "0x030000")
		if got := FindVFIOCard(); got != "0000:01:00.0" {
			t.Fatalf("FindVFIOCard() = %q, want 0000:01:00.0", got)
		}
	})
	t.Run("bound to another driver", func(t *testing.T) {
		root := t.TempDir()
		setSysfs(t, root)
		pciFunction(t, root, "0000:01:00.0", "nvidia", "0x10de", "0x030000")
		if got := FindVFIOCard(); got != "" {
			t.Fatalf("FindVFIOCard() = %q, want empty", got)
		}
	})
	t.Run("not a display function", func(t *testing.T) {
		root := t.TempDir()
		setSysfs(t, root)
		pciFunction(t, root, "0000:01:00.0", "vfio-pci", "0x10de", "0x020000")
		if got := FindVFIOCard(); got != "" {
			t.Fatalf("FindVFIOCard() = %q, want empty", got)
		}
	})
	t.Run("another vendor", func(t *testing.T) {
		root := t.TempDir()
		setSysfs(t, root)
		pciFunction(t, root, "0000:01:00.0", "vfio-pci", "0x8086", "0x030000")
		if got := FindVFIOCard(); got != "" {
			t.Fatalf("FindVFIOCard() = %q, want empty", got)
		}
	})
	t.Run("two matches", func(t *testing.T) {
		root := t.TempDir()
		setSysfs(t, root)
		pciFunction(t, root, "0000:01:00.0", "vfio-pci", "0x10de", "0x030000")
		pciFunction(t, root, "0000:02:00.0", "vfio-pci", "0x10de", "0x030000")
		if got := FindVFIOCard(); got != "" {
			t.Fatalf("FindVFIOCard() = %q, want empty", got)
		}
	})
}
