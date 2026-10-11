package host

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	w := func(n, s string) { os.WriteFile(filepath.Join(dir, n), []byte(s), 0o644) }
	w("stat", "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 1\ncpu1 1\n")
	w("meminfo", "MemTotal: 1000 kB\nMemAvailable: 250 kB\nSwapTotal: 100 kB\nSwapFree: 40 kB\n")
	w("loadavg", "1.50 0.75 0.25 2/300 99\n")
	r := &Reader{ProcRoot: dir}
	s := r.Read()
	if !s.Supported || s.Cores != 2 || s.RAMUsed != 750*1024 || s.SwapUsed != 60*1024 || s.Load[0] != 1.5 || s.CPU != 0 {
		t.Fatalf("first read = %+v", s)
	}
	w("stat", "cpu  150 0 150 900 0 0 0 0 0 0\ncpu0 1\ncpu1 1\n") // +100 busy, +100 idle
	if s = r.Read(); s.CPU != 50 {
		t.Fatalf("cpu = %v, want 50", s.CPU)
	}
	if (&Reader{ProcRoot: filepath.Join(dir, "none")}).Read().Supported {
		t.Fatal("missing /proc must read as unsupported")
	}
}
