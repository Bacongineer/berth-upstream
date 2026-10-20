package storage

import (
	"errors"
	"testing"
)

func TestFloorMath(t *testing.T) {
	r := Root{Path: "/", FS: "ext4", Total: 1000, Free: 300, Used: 700}
	for _, tc := range []struct {
		need  uint64
		frac  float64
		under bool
	}{
		{250, 0.1, true},  // 300 left, 250 + the 100 floor would cross it
		{200, 0.1, false}, // lands exactly on the floor
		{199, 0.1, false},
		{0, 0.1, false},
		{999, 0, false}, // a floor of 0 disables the guard
	} {
		got, err := r.WouldGoUnderFloor(tc.need, tc.frac)
		if err != nil {
			t.Fatalf("need %d frac %g: %v", tc.need, tc.frac, err)
		}
		if got != tc.under {
			t.Errorf("need %d frac %g: under = %v, want %v", tc.need, tc.frac, got, tc.under)
		}
	}
	if _, err := r.WouldGoUnderFloor(1, -0.1); err == nil {
		t.Error("a negative floor must not pass validation")
	}
	if _, err := r.WouldGoUnderFloor(1, 2); err == nil {
		t.Error("a floor over the whole root must not pass validation")
	}
	if got := r.FloorBytes(0.1); got != 100 {
		t.Errorf("FloorBytes = %d, want 100", got)
	}
	if got := r.FloorBytes(0); got != 0 {
		t.Errorf("FloorBytes(0) = %d, want 0", got)
	}
}

func TestUndetectableRoot(t *testing.T) {
	r := Root{Path: "/", FS: "unknown"}
	under, err := r.WouldGoUnderFloor(1, 0.1)
	if !errors.Is(err, ErrUndetectable) || under {
		t.Errorf("under=%v err=%v, want the undetectable root to refuse no verdict", under, err)
	}
}

func TestInfo(t *testing.T) {
	dir := t.TempDir()
	r, err := Info(dir)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if r.Path != dir {
		t.Errorf("Path = %q, want %q", r.Path, dir)
	}
	if r.Detectable() {
		if r.FS == "" || r.FS == "unknown" {
			t.Errorf("FS = %q, want a name", r.FS)
		}
		if r.Free > r.Total {
			t.Errorf("free %d over total %d", r.Free, r.Total)
		}
		if r.Used != r.Total-r.Free {
			t.Errorf("used %d, want total minus free", r.Used)
		}
	}
	if _, err := Info(dir + "/no-such-directory"); err == nil {
		t.Error("a missing path must come back as an error")
	}
}
