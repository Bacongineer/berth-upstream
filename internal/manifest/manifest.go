// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package manifest records what berth pull fetched: which source, its exact
// revision, and every file's size and sha256. The record is written beside
// the files it describes, so another machine or a later run can re-check them
// without asking the source again.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/T-Crypt/berth/internal/fsx"
)

// Name sits beside pulled files: what was pulled, from which exact revision,
// and the hash of every file, so integrity can be re-checked later without
// asking the source.
const Name = ".berth-manifest.json"

// The sources a pull may come from.
const (
	SourceHF      = "hf"
	SourceCivitai = "civitai"
)

// File is one file of a pull and what the source published for it.
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Manifest is one source record in one directory.
type Manifest struct {
	Source string `json:"source"` // hf | civitai
	// Name is the source's own name for what was pulled: a Hub repo or a
	// CivitAI model name.
	Name string `json:"name"`
	// Ref pins the exact revision: a Hub commit sha or a CivitAI version id.
	Ref    string    `json:"ref"`
	Files  []File    `json:"files"`
	Pulled time.Time `json:"pulled"`
}

// ReadManifest reads dir's record. A record written before the source field
// existed is read as a Hub pull with the repo and commit it then recorded.
func ReadManifest(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, Name))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", Name, err)
	}
	if m.Source == "" {
		m.Source = SourceHF
		var legacy struct {
			Repo   string `json:"repo"`
			Commit string `json:"commit"`
		}
		if err := json.Unmarshal(b, &legacy); err == nil {
			if m.Name == "" {
				m.Name = legacy.Repo
			}
			if m.Ref == "" {
				m.Ref = legacy.Commit
			}
		}
	}
	return m, nil
}

// WriteManifest merges files into dir's record: a second pull of another
// quant from the same record adds to it rather than forgetting the first.
// Pulls land in per-source directories (the Hub repo, the CivitAI model), so
// one directory carries one record.
func WriteManifest(dir string, m Manifest) error {
	if m.Source == "" {
		m.Source = SourceHF
	}
	old, err := ReadManifest(dir)
	if err == nil && old.Source == m.Source && old.Name == m.Name {
		seen := map[string]bool{}
		for _, f := range m.Files {
			seen[f.Path] = true
		}
		for _, f := range old.Files {
			if !seen[f.Path] {
				m.Files = append(m.Files, f)
			}
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFile(filepath.Join(dir, Name), append(b, '\n'), 0o644)
}

// Check is one file's verdict.
type Check struct {
	Path   string `json:"path"`
	Status string `json:"status"` // ok | missing | size | hash | unhashed
	Detail string `json:"detail,omitempty"`
}

// Verify re-checks every file a record lists: present, the right size and,
// where the source published a sha256, the right hash.
func Verify(dir string, prog func(file string, done, total int64)) ([]Check, error) {
	m, err := ReadManifest(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s: only files berth pull fetched can be verified", dir, Name)
	}
	if err != nil {
		return nil, err
	}
	var out []Check
	for _, f := range m.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		st, err := os.Stat(p)
		switch {
		case err != nil:
			out = append(out, Check{Path: f.Path, Status: "missing"})
			continue
		case st.Size() != f.Size:
			out = append(out, Check{Path: f.Path, Status: "size", Detail: fmt.Sprintf("%d bytes, want %d", st.Size(), f.Size)})
			continue
		case f.SHA256 == "":
			out = append(out, Check{Path: f.Path, Status: "unhashed", Detail: "the source publishes no sha256 for this file; size matches"})
			continue
		}
		var cb func(done, total int64)
		if prog != nil {
			cb = func(done, total int64) { prog(f.Path, done, total) }
		}
		sum, err := HashFile(p, cb)
		if err != nil {
			return out, err
		}
		if sum != f.SHA256 {
			out = append(out, Check{Path: f.Path, Status: "hash", Detail: "sha256 " + sum + ", want " + f.SHA256})
			continue
		}
		out = append(out, Check{Path: f.Path, Status: "ok"})
	}
	return out, nil
}

// HashFile is the sha256 of a file, reporting progress as it reads.
func HashFile(p string, prog func(done, total int64)) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var r io.Reader = f
	if prog != nil {
		r = &progReader{r: f, total: st.Size(), prog: prog}
	}
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type progReader struct {
	r     io.Reader
	n     int64
	total int64
	prog  func(done, total int64)
	last  time.Time
}

func (p *progReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if time.Since(p.last) > 500*time.Millisecond || err == io.EOF {
		p.last = time.Now()
		p.prog(p.n, p.total)
	}
	return n, err
}
