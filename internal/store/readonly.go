package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoStore is a harbor database that is not there. `berth models` reads it as
// "no usage yet", not as a failure: a node that has never run a harbor has
// nothing to report and nothing wrong with it.
var ErrNoStore = errors.New("no harbor database")

// RequestRow is one requests row, in the shape a reader wants it.
type RequestRow struct {
	T      time.Time
	Engine string
	Model  string
	Prompt float64
	Output float64
}

// OpenReadOnly opens an existing harbor database for reading only:
// the CLI asks what the harbor saw, and never creates a database, migrates a
// schema or writes a row. SQLite refuses a write to a mode=ro connection, so a
// bug here cannot touch the collector's file, and a file that does not exist
// stays absent (ErrNoStore) instead of being created empty.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", path, ErrNoStore)
		}
		return nil, err
	}
	// The DSN is a URI, so the separators are forward ones even on the Windows
	// node; mode=ro is the whole point and the busy timeout is what lets a read
	// wait out the collector's transaction instead of failing on it.
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Store{path: path, retention: DefaultRetention, db: db}, nil
}

// RequestsSince returns the stored requests at or after since, oldest first.
// A database written by an older schema has no requests table, and that reads
// as no rows: the tables are created by Open, which a read-only open never is.
func (s *Store) RequestsSince(since time.Time) ([]RequestRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(`SELECT t, engine, model, prompt, output FROM requests WHERE t >= ? ORDER BY t`,
		since.Unix())
	if err != nil {
		if isMissingTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var out []RequestRow
	for rows.Next() {
		var (
			t      int64
			engine string
			model  sql.NullString
			prompt sql.NullFloat64
			output sql.NullFloat64
		)
		if err := rows.Scan(&t, &engine, &model, &prompt, &output); err != nil {
			return nil, err
		}
		r := RequestRow{
			T:      time.Unix(t, 0).UTC(),
			Engine: engine,
			Model:  model.String,
		}
		if prompt.Valid {
			r.Prompt = prompt.Float64
		}
		if output.Valid {
			r.Output = output.Float64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// isMissingTable is the one schema error a reader treats as empty rather than
// as broken: a database file whose requests table was never created.
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
