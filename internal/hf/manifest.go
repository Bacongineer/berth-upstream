package hf

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ManifestName sits beside pulled files: what was pulled, from which exact
// commit, and the hash of every file, so integrity can be re-checked later
// without asking the Hub.
const ManifestName = ".berth-manifest.json"

type Manifest struct {
	Repo     string    `json:"repo"`
	Revision string    `json:"revision"`
	Commit   string    `json:"commit"`
	Files    []File    `json:"files"`
	Pulled   time.Time `json:"pulled"`
}

// WriteManifest merges files into dir's manifest: a second pull of another
// quant from the same repo adds to it rather than forgetting the first.
func WriteManifest(dir string, m Manifest) error {
	old, err := ReadManifest(dir)
	if err == nil && old.Repo == m.Repo {
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
	tmp := filepath.Join(dir, ManifestName+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ManifestName))
}

func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", ManifestName, err)
	}
	return m, nil
}

// Check is one file's verdict.
type Check struct {
	Path   string `json:"path"`
	Status string `json:"status"` // ok | missing | size | hash | unhashed
	Detail string `json:"detail,omitempty"`
}

// Verify re-checks every file a manifest lists: present, the right size,
// and, for LFS files, the right sha256.
func Verify(dir string, prog func(file string, done, total int64)) ([]Check, error) {
	m, err := ReadManifest(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s has no %s: only files berth pull fetched can be verified", dir, ManifestName)
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
			out = append(out, Check{Path: f.Path, Status: "unhashed", Detail: "the Hub publishes no sha256 for this file; size matches"})
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
