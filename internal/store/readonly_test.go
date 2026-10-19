package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/proto"
)

func TestOpenReadOnlyRefusesToCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbor.db")
	_, err := OpenReadOnly(path)
	if !errors.Is(err, ErrNoStore) {
		t.Fatalf("open = %v, want ErrNoStore", err)
	}
	// The point of mode=ro: asking for a database that is not there must not be
	// the thing that creates it.
	if fi, err := os.Stat(path); err == nil {
		t.Fatalf("a read-only open created %s (%d bytes)", path, fi.Size())
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(path + "*"); len(left) != 0 {
		t.Fatalf("a read-only open left %v behind", left)
	}
}

func TestOpenReadOnlyReadsAndRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "harbor.db")
	w, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := w.IngestRequests([]proto.Request{
		{ID: "a", Engine: "llama-swap", Model: "Qwen3-8B", T: now, Prompt: 120, Output: 30},
		{ID: "b", Engine: "strata", Model: "nomic-embed", T: now.Add(-time.Hour), Prompt: 7},
		{ID: "c", Engine: "llama-swap", T: now}, // a request that named no model
	}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	rows, err := r.RequestsSince(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %v", len(rows), rows)
	}
	// Oldest first, and the columns a reader wants are the ones it asked for.
	if rows[0].Model != "nomic-embed" || rows[1].Model != "Qwen3-8B" {
		t.Errorf("rows = %v, want oldest first", rows)
	}
	if rows[1].Prompt != 120 || rows[1].Output != 30 || rows[1].Engine != "llama-swap" {
		t.Errorf("row = %+v, want the engine's own numbers", rows[1])
	}
	if !rows[1].T.Equal(now) {
		t.Errorf("t = %v, want %v", rows[1].T, now)
	}
	// A window cuts them.
	window, err := r.RequestsSince(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 2 {
		t.Errorf("window rows = %d, want the two this hour", len(window))
	}

	// And a write through this handle is refused by SQLite itself.
	if _, err := r.db.Exec(`INSERT INTO requests (id, t, engine) VALUES ('z', 1, 'x')`); err == nil {
		t.Error("a read-only handle accepted a write")
	}
}

func TestRequestsSinceNoTable(t *testing.T) {
	// A database from before the requests table is a reader's empty answer, not
	// a broken one.
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE requests`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rows, err := r.RequestsSince(time.Time{})
	if err != nil {
		t.Fatalf("a database with no requests table = %v, want no rows and no error", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %v, want none", rows)
	}
}
