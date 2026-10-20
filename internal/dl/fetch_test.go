package dl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// serve hands out body with real Range support, like a file host does.
func serve(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := int64(0)
		if rng := r.Header.Get("Range"); rng != "" {
			if len(rng) > 6 && rng[:6] == "bytes=" {
				if err := parseRangeStart(rng[6:], &start); err != nil {
					http.Error(w, "bad range", http.StatusBadRequest)
					return
				}
			}
		}
		w.Header().Set("Content-Length", itoa(int64(len(body))-start))
		if start > 0 {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(body[start:])
	}))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func parseRangeStart(s string, n *int64) error {
	if s == "" {
		return errors.New("empty range")
	}
	var v int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return errors.New("not a number")
		}
		v = v*10 + int64(c-'0')
	}
	*n = v
	return nil
}

func TestFetchWholeAndVerify(t *testing.T) {
	body := make([]byte, 200_000)
	for i := range body {
		body[i] = byte(i % 251)
	}
	srv := serve(t, body)
	defer srv.Close()
	dir := t.TempDir()
	spec := FetchSpec{URL: srv.URL + "/m.bin", Part: filepath.Join(dir, "m.bin.part"),
		Dest: filepath.Join(dir, "m.bin"), Size: int64(len(body)), SHA256: sum(body)}
	if err := Fetch(context.Background(), srv.Client(), spec); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got, err := os.ReadFile(spec.Dest)
	if err != nil || string(got) != string(body) {
		t.Fatalf("destination = %d bytes, %v; want the body", len(got), err)
	}
	if _, err := os.Stat(spec.Part); !os.IsNotExist(err) {
		t.Error("the part must not survive a finished download")
	}
}

// A part left by an interrupted download is where the next attempt begins.
func TestFetchResumes(t *testing.T) {
	body := make([]byte, 300_000)
	for i := range body {
		body[i] = byte(i % 199)
	}
	var ranges atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := int64(0)
		if rng := r.Header.Get("Range"); len(rng) > 6 && rng[:6] == "bytes=" {
			_ = parseRangeStart(rng[6:], &start)
			ranges.Add(1)
		}
		w.Header().Set("Content-Length", itoa(int64(len(body))-start))
		if start > 0 {
			w.WriteHeader(http.StatusPartialContent)
		}
		_, _ = w.Write(body[start:])
	}))
	defer srv.Close()
	dir := t.TempDir()
	part := filepath.Join(dir, "m.part")
	if err := os.WriteFile(part, body[:100_000], 0o644); err != nil {
		t.Fatal(err)
	}
	spec := FetchSpec{URL: srv.URL + "/m", Part: part, Dest: filepath.Join(dir, "m"),
		Size: int64(len(body)), SHA256: sum(body)}
	if err := Fetch(context.Background(), srv.Client(), spec); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if ranges.Load() != 1 {
		t.Errorf("range requests = %d, want exactly the resume", ranges.Load())
	}
	got, _ := os.ReadFile(spec.Dest)
	if string(got) != string(body) {
		t.Error("the resumed file differs from the source's")
	}
}

// A host that lies about the content is refused, with the part cleaned up.
func TestFetchRefusesMismatch(t *testing.T) {
	body := []byte("0123456789")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not the file"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	spec := FetchSpec{URL: srv.URL, Part: filepath.Join(dir, "m.part"), Dest: filepath.Join(dir, "m"),
		Size: int64(len(body)), SHA256: sum(body)}
	err := Fetch(context.Background(), srv.Client(), spec)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("err = %v, want the integrity failure", err)
	}
	if _, err := os.Stat(spec.Part); !os.IsNotExist(err) {
		t.Error("a mismatched part must be deleted, not kept for the next attempt")
	}
}

// Progress reports as the copy runs and lands on the total at the end.
func TestFetchProgress(t *testing.T) {
	body := make([]byte, 50_000)
	srv := serve(t, body)
	defer srv.Close()
	var last int64
	var calls int64
	spec := FetchSpec{URL: srv.URL, Part: "/tmp/dltest/part", Dest: "/tmp/dltest/d",
		Size: int64(len(body)), SHA256: sum(body),
		Prog: func(done, total int64) {
			if done > last {
				last = done
			}
			calls++
		}}
	t.Cleanup(func() { _ = os.RemoveAll("/tmp/dltest") })
	if err := os.MkdirAll("/tmp/dltest", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), srv.Client(), spec); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || last != int64(len(body)) {
		t.Errorf("progress: %d calls, last %d, want a report ending at %d", calls, last, len(body))
	}
}

// A client that drops the connection twice then delivers: the retry gets
// the file, and an integrity failure is not retried.
func TestRetry(t *testing.T) {
	body := []byte("hello world")
	fails := int64(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt64(&fails) > 0 {
			atomic.AddInt64(&fails, -1)
			http.Error(w, "drop", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dir := t.TempDir()
	spec := FetchSpec{URL: srv.URL, Part: filepath.Join(dir, "m.part"), Dest: filepath.Join(dir, "m"),
		Size: int64(len(body)), SHA256: sum(body)}
	if err := Retry(context.Background(), srv.Client(), spec, 5); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	got, _ := os.ReadFile(spec.Dest)
	if string(got) != string(body) {
		t.Error("retried file differs")
	}
	// Integrity: one attempt, no backoff.
	spec2 := spec
	spec2.Size = 99 // the server sends 11 bytes: a size mismatch
	if err := Retry(context.Background(), srv.Client(), spec2, 5); !errors.Is(err, ErrIntegrity) {
		t.Errorf("size mismatch = %v, want the integrity failure with no retry", err)
	}
}
