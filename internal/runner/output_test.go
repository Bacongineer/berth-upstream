//go:build unix

package runner

import (
	"bytes"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// Output gets the engine's own stdout and stderr, and an engine that leaves
// a grandchild holding the copy's pipes still ends the run when it exits:
// the lease must not wait on a process the engine left behind.
func TestOutputCopiesTheEngineAndDoesNotHangOnAGrandchild(t *testing.T) {
	var say, out safeBuf
	missing := filepath.Join(t.TempDir(), "none.sock") // no daemon: the unleased path, same command()
	start := time.Now()
	code := Run(Options{Socket: missing, Engine: "x", Stderr: &say, Output: &out,
		Argv: []string{"sh", "-c", `echo to-stdout; echo to-stderr >&2; sleep 20 & exit 3`}})
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("Run took %s: it waited on the grandchild", took)
	}
	if code != 3 {
		t.Fatalf("exit %d, want the engine's 3", code)
	}
	if s := out.String(); !strings.Contains(s, "to-stdout") || !strings.Contains(s, "to-stderr") {
		t.Fatalf("output copy %q misses the engine's lines", s)
	}
	if strings.Contains(out.String(), "berth:") {
		t.Fatalf("berth's own lines leaked into the engine copy: %q", out.String())
	}
}
