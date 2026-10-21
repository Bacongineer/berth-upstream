// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/pagecache"
)

// modelTree is a directory with two model files and a subdirectory, the
// shape a downloaded model arrives in.
func modelTree(t *testing.T) (dir, one, two string) {
	t.Helper()
	dir = t.TempDir()
	one, two = filepath.Join(dir, "one.gguf"), filepath.Join(dir, "two.gguf")
	for _, f := range []struct {
		path, body string
	}{
		{one, "aaaa"},
		{two, "bbbbbbb"},
		{filepath.Join(dir, "subdir", "inner.gguf"), "never asked for"},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, one, two
}

// fakeAsk stands in for the kernel: it records every path it is called with
// and answers each one with whatever the test decides.
type fakeAsk struct {
	asked []string
	err   func(path string) error
}

func (f *fakeAsk) Prefetch(path string) error {
	f.asked = append(f.asked, path)
	if f.err != nil {
		return f.err(path)
	}
	return nil
}

// fakeGateway is a gateway whose only moving part is the ask, beside a
// journal the test can read.
func fakeGateway(t *testing.T, ask func(string) error) (*Gateway, *fakeAsk, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	f := &fakeAsk{err: ask}
	g := &Gateway{Log: slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Prefetch: f.Prefetch}
	return g, f, buf
}

func TestPrefetchFiles(t *testing.T) {
	dir, one, two := modelTree(t)
	gone := filepath.Join(dir, "gone.gguf")
	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
		bytes uint64
	}{
		{name: "nothing named", paths: nil},
		{name: "files keep the order they are listed in", paths: []string{two, one}, want: []string{two, one}, bytes: 11},
		{name: "a directory is its own files, not its subdirectory", paths: []string{dir}, want: []string{one, two}, bytes: 11},
		{name: "a path that is gone is still asked for", paths: []string{gone}, want: []string{gone}},
		{name: "a file beside the directory it lives in is asked for twice", paths: []string{dir, one}, want: []string{one, two, one}, bytes: 15},
	} {
		files, bytes := prefetchFiles(tc.paths)
		if !slices.Equal(files, tc.want) || bytes != tc.bytes {
			t.Errorf("%s: files = %v of %d bytes, want %v of %d", tc.name, files, bytes, tc.want, tc.bytes)
		}
	}
}

// A models directory of links into a blob store, as a Hugging Face snapshot
// is laid out, is a directory of files worth reading.
func TestPrefetchFilesFollowsLinks(t *testing.T) {
	dir, one, _ := modelTree(t)
	link := filepath.Join(dir, "link.gguf")
	if err := os.Symlink(one, link); err != nil {
		t.Skipf("this platform cannot make a symlink: %v", err)
	}
	files, bytes := prefetchFiles([]string{dir})
	if !slices.Contains(files, link) || bytes != 15 {
		t.Errorf("files = %v of %d bytes, want %q among them", files, bytes, link)
	}
}

func TestPrefetchAsksEveryFileOnce(t *testing.T) {
	dir, one, two := modelTree(t)
	g, ask, logs := fakeGateway(t, nil)
	g.prefetch(&config.Engine{Name: "mine", Prefetch: []string{dir}})
	if want := []string{one, two}; !slices.Equal(ask.asked, want) {
		t.Errorf("asked %v, want %v", ask.asked, want)
	}
	for _, want := range []string{`msg="gateway prefetching engine files"`, "engine=mine", "files=2", "bytes=11"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("journal lacks %s: %s", want, logs)
		}
	}
	if n := strings.Count(logs.String(), "level=INFO"); n != 1 {
		t.Errorf("%d INFO lines, want the one: %s", n, logs)
	}
}

// An engine whose operator named no files asks for nothing and says nothing:
// every other engine's start stays exactly as quiet as it was.
func TestPrefetchWithoutAListIsSilent(t *testing.T) {
	g, ask, logs := fakeGateway(t, func(string) error {
		t.Error("asked for a file no engine named")
		return nil
	})
	g.prefetch(&config.Engine{Name: "mine"})
	if len(ask.asked) != 0 {
		t.Errorf("asked %v, want nothing", ask.asked)
	}
	if logs.Len() != 0 {
		t.Errorf("journal = %s, want nothing", logs)
	}
}

// A file that cannot be asked for is one WARN naming it, and the rest of the
// list is still asked for.
func TestPrefetchNamesEachFailure(t *testing.T) {
	dir, one, two := modelTree(t)
	gone := filepath.Join(dir, "gone.gguf")
	g, ask, logs := fakeGateway(t, func(p string) error {
		if p == one {
			return errors.New("permission denied")
		}
		return nil
	})
	g.prefetch(&config.Engine{Name: "mine", Prefetch: []string{dir, gone}})
	if want := []string{one, two, gone}; !slices.Equal(ask.asked, want) {
		t.Errorf("asked %v, want %v", ask.asked, want)
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Fatalf("%d WARN lines, want one: %s", n, logs)
	}
	for _, want := range []string{`msg="gateway prefetch failed"`, "engine=mine", `err="permission denied"`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("journal lacks %s: %s", want, logs)
		}
	}
	if !logged(logs.String(), "path", one) {
		t.Errorf("journal does not name the file: %s", logs)
	}
}

// logged reports whether a journal line carries value under key. slog quotes
// a value with a space in it and leaves the rest, and a temp directory is
// spelled differently on every platform.
func logged(journal, key, value string) bool {
	return strings.Contains(journal, key+"="+value) ||
		strings.Contains(journal, key+`="`+value+`"`)
}

// A platform with no kernel page-cache interface says so for every file.
// That is a fact about the box, so it is not a warning, and one file is
// enough to have learned it.
func TestPrefetchReportsNoPageCacheOnce(t *testing.T) {
	g, ask, logs := fakeGateway(t, func(string) error { return pagecache.ErrUnsupported })
	g.prefetch(&config.Engine{Name: "mine", Prefetch: []string{"/models/a.gguf", "/models/b.gguf"}})
	if len(ask.asked) != 1 {
		t.Errorf("%d asks, want the one that learned the answer: %v", len(ask.asked), ask.asked)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("a platform fact is not a warning: %s", logs)
	}
	if !strings.Contains(logs.String(), "page-cache interface") {
		t.Errorf("nothing says what the box lacks: %s", logs)
	}
}
