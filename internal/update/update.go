// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package update asks GitHub whether a newer berth release exists and,
// when the operator asks, downloads and verifies this machine's release
// binary.
//
// The check is the one contact Berth makes on its own: a single GET to
// the GitHub API's releases/latest, at most once every 24 h (the answer
// is cached in the state directory) and only while [update] check in
// berth.toml is on, which is the default. `berth update` reaches GitHub
// because the operator typed it: the same check, plus the release's
// binary for this machine and its SHA256SUMS. Berth sends nothing but
// its own version in the check's User-Agent.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/fsx"
)

// APIURL is the endpoint Check asks. The release workflow publishes its
// binaries on the same repository, so the release's page and assets come
// out of the one answer. Tests point it at a local server.
var APIURL = "https://api.github.com/repos/T-Crypt/berth/releases/latest"

// CacheTTL is how long a check's answer stays the one the node serves.
const CacheTTL = 24 * time.Hour

// checkTimeout bounds the one GET; a hung network must not hang a start.
const checkTimeout = 10 * time.Second

// Result is the newest release and whether it is newer than the version
// it was checked against.
type Result struct {
	Latest string            `json:"latest"` // e.g. v0.3.1
	URL    string            `json:"url"`    // the release's page on GitHub
	Newer  bool              `json:"newer"`
	Assets map[string]string `json:"assets,omitempty"` // asset name to download URL
	At     time.Time         `json:"at"`               // when the check was made
}

// release is the shape of the GitHub answer that matters.
type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Check asks for the newest release: one GET, a 10 s bound, and no
// headers beyond a User-Agent of berth/<current>. A current that is not
// a release version (a dev build) is never behind a release, so Newer
// comes back false and no update is offered.
func Check(ctx context.Context, current string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, APIURL, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("User-Agent", "berth/"+current)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("ask %s: %w", APIURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("%s answered %s", APIURL, resp.Status)
	}
	var rel release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rel); err != nil {
		return Result{}, fmt.Errorf("read the release: %w", err)
	}
	if rel.TagName == "" {
		return Result{}, fmt.Errorf("the release names no tag")
	}
	assets := make(map[string]string, len(rel.Assets))
	for _, a := range rel.Assets {
		assets[a.Name] = a.URL
	}
	return Result{
		Latest: rel.TagName,
		URL:    rel.HTMLURL,
		Newer:  Newer(rel.TagName, current),
		Assets: assets,
		At:     time.Now(),
	}, nil
}

// version is one release's three numbers and its pre-release label, if
// any ("rc" in 0.3.0-rc); build metadata after "+" is ignored.
type version struct {
	major, minor, patch int
	pre                 string
}

// parseVersion reads v1.2.3, 1.2.3, 1.2.3-rc or 1.2.3+build. Anything
// else, a dev build or a tag with no numbers, is not a version: rather
// than guess, Newer stays false.
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	v := version{pre: pre}
	nums := [3]*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version{}, false
		}
		*nums[i] = n
	}
	return v, true
}

// after is semver precedence: the numbers first, then a release ranks
// above any pre-release of the same numbers (0.3.0 after 0.3.0-rc).
func (x version) after(y version) bool {
	switch {
	case x.major != y.major:
		return x.major > y.major
	case x.minor != y.minor:
		return x.minor > y.minor
	case x.patch != y.patch:
		return x.patch > y.patch
	case x.pre == "" || y.pre == "":
		return x.pre == "" && y.pre != ""
	default:
		return x.pre > y.pre
	}
}

// Newer says whether a is a release later than b. A that is not a
// version is never newer; a b that is not one (a dev build) is never
// behind a release, so no update is offered to a tree past the release
// line.
func Newer(a, b string) bool {
	va, ok := parseVersion(a)
	if !ok {
		return false
	}
	if va.pre != "" {
		return false // a pre-release is never offered as an update
	}
	vb, ok := parseVersion(b)
	if !ok {
		return false
	}
	return va.after(vb)
}

// LoadCache reads the last check's answer kept at path. The bool is
// false when there is no answer or it cannot be read: a check that
// cannot load its cache simply re-asks.
func LoadCache(path string) (Result, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, false
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, false
	}
	return r, true
}

// StoreCache keeps the answer at path, atomically and 0o600, so a crash
// leaves either the old answer or the new one.
func StoreCache(path string, r Result) error {
	b, err := json.Marshal(&r)
	if err != nil {
		return err
	}
	return fsx.WriteFile(path, b, 0o600)
}

// Refresh is how both the harbor and berth update reach an answer: the
// cache while it is under CacheTTL old, the one GET when it is not. A
// cached answer is re-priced against the version running now, so an
// update of the binary itself does not leave a stale "newer" behind. A
// GET that fails leaves the last answer standing; with no answer at all
// it returns the error.
func Refresh(ctx context.Context, path, current string) (Result, error) {
	cached, have := LoadCache(path)
	if have {
		cached.Newer = Newer(cached.Latest, current)
		if time.Since(cached.At) < CacheTTL {
			return cached, nil
		}
	}
	fresh, err := Check(ctx, current)
	if err != nil {
		if have {
			return cached, nil
		}
		return Result{}, err
	}
	if err := StoreCache(path, fresh); err != nil {
		return fresh, nil // the answer is good; only its keeping failed
	}
	return fresh, nil
}

// Fetch is the answer for a command the operator typed: it always asks
// GitHub, keeps the answer for the harbor, and only falls back to the last
// kept answer when the GET fails. A cached "no newer release" from before
// a release must never answer `berth update`.
func Fetch(ctx context.Context, path, current string) (Result, error) {
	fresh, err := Check(ctx, current)
	if err != nil {
		if cached, have := LoadCache(path); have {
			cached.Newer = Newer(cached.Latest, current)
			return cached, nil
		}
		return Result{}, err
	}
	_ = StoreCache(path, fresh) // the answer is good; only its keeping may fail
	return fresh, nil
}

// Download fetches url into path: a temp file in path's directory,
// fsynced, then renamed over it, so a dead link or a crash leaves no
// half file.
func Download(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // a no-op after the rename
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Verify checks one file's sha256 against the sums file a release
// publishes, a "<64 hex>  <name>" line per asset. name is the line the
// file's asset carries in the sums, file the downloaded bytes: the
// download lands beside the binary as binary.new, which the sums do not
// know. The line must be there and its hash must match; anything else is
// a refusal, not a warning.
func Verify(sums []byte, name, file string) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no sha256 for %s in the sums file", name)
	}
	got, err := sha256File(file)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("sha256 of %s is %s, the release records %s", name, got, want)
	}
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
