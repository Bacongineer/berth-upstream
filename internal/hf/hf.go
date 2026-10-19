// Package hf downloads model files from the Hugging Face Hub and proves
// they arrived whole: every LFS file's size and sha256 are checked against
// what the Hub publishes for that exact commit, a partial download resumes
// instead of starting over, and a manifest beside the files lets `berth
// verify` re-check them later.
package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/manifest"
)

// Endpoint is the Hub; HF_ENDPOINT overrides it (mirrors, tests).
func Endpoint() string {
	if e := os.Getenv("HF_ENDPOINT"); e != "" {
		return strings.TrimRight(e, "/")
	}
	return "https://huggingface.co"
}

// Ref is what to pull: a repo, an optional selector, an optional revision.
type Ref struct {
	Repo     string // org/name
	Selector string // a file path, a glob, or a quant such as Q4_K_M; "" = every GGUF
	Revision string // branch, tag or commit; "" = main
}

var repoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ParseRef reads hf:org/repo[:selector][@revision]. The hf: prefix and a
// full https://huggingface.co/ URL are both accepted.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "hf:")
	s = strings.TrimPrefix(s, "https://huggingface.co/")
	var r Ref
	if i := strings.LastIndex(s, "@"); i > 0 {
		s, r.Revision = s[:i], s[i+1:]
	}
	parts := strings.SplitN(s, ":", 2)
	r.Repo = strings.Trim(parts[0], "/")
	if len(parts) == 2 {
		r.Selector = parts[1]
	}
	if !repoRE.MatchString(r.Repo) {
		return Ref{}, fmt.Errorf("%q is not a Hub repo (want hf:org/name[:quant or file][@revision])", s)
	}
	if r.Revision == "" {
		r.Revision = "main"
	}
	return r, nil
}

// File is one file in a repo at a commit, as the Hub reports it. SHA256 is
// set for LFS files (every model weight); small git files have none. It is
// the manifest's file, so a tree entry goes straight into a record.
type File = manifest.File

// Client talks to the Hub. Token is optional (gated and private repos).
type Client struct {
	HTTP  *http.Client
	Token string
	Base  string // "" = Endpoint()
}

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return Endpoint()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 0} // downloads take as long as they take; ctx bounds them
}

func (c *Client) get(ctx context.Context, u string, hdr map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "berth")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return c.http().Do(req)
}

// Commit resolves a revision to the commit it points at now, so every file
// of one pull comes from the same commit even if the branch moves mid-way.
func (c *Client) Commit(ctx context.Context, repo, rev string) (string, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/api/models/%s/revision/%s", c.base(), repo, url.PathEscape(rev)), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := status(resp, repo); err != nil {
		return "", err
	}
	var r struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.SHA == "" {
		return "", fmt.Errorf("%s@%s: no commit in the Hub's answer", repo, rev)
	}
	return r.SHA, nil
}

// Tree lists every file in repo at commit.
func (c *Client) Tree(ctx context.Context, repo, commit string) ([]File, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/api/models/%s/tree/%s?recursive=true", c.base(), repo, commit), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := status(resp, repo); err != nil {
		return nil, err
	}
	var raw []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		LFS  *struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: tree: %w", repo, err)
	}
	var out []File
	for _, e := range raw {
		if e.Type != "file" {
			continue
		}
		f := File{Path: e.Path, Size: e.Size}
		if e.LFS != nil {
			f.SHA256, f.Size = e.LFS.OID, e.LFS.Size
		}
		out = append(out, f)
	}
	return out, nil
}

func status(resp *http.Response, repo string) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: %s: gated or private; accept its terms on the Hub and set HF_TOKEN", repo, resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("%s: not found on the Hub (repo, revision or file)", repo)
	}
	return fmt.Errorf("%s: the Hub answered %s", repo, resp.Status)
}

var splitRE = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)

// Select picks the files a selector names. No selector: every GGUF (fine
// for a single-file repo; a repo with many quants asks for one). A selector
// that is an exact path or a glob matches paths; anything else is a quant
// name matched case-insensitively in GGUF file names, split parts included.
// mmproj (vision projector) files come along with the model unless the
// selector names something else.
func Select(files []File, sel string) ([]File, error) {
	var ggufs []File
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".gguf") {
			ggufs = append(ggufs, f)
		}
	}
	var out []File
	switch {
	case sel == "":
		out = ggufs
	case strings.ContainsAny(sel, "*?[") || strings.Contains(sel, "/") || strings.HasSuffix(sel, ".gguf") || strings.Contains(sel, "."):
		for _, f := range files {
			if ok, _ := path.Match(sel, f.Path); ok || f.Path == sel {
				out = append(out, f)
			}
		}
	default:
		q := strings.ToLower(sel)
		for _, f := range ggufs {
			base := strings.ToLower(path.Base(f.Path))
			if strings.Contains(base, "mmproj") {
				continue
			}
			if strings.Contains(base, q) {
				out = append(out, f)
			}
		}
		if len(out) > 0 {
			for _, f := range ggufs {
				if strings.Contains(strings.ToLower(path.Base(f.Path)), "mmproj") {
					out = append(out, f)
					break // one projector is enough; the F16 one sorts first on most repos
				}
			}
		}
	}
	if len(out) == 0 {
		var names []string
		for _, f := range ggufs {
			names = append(names, f.Path)
		}
		return nil, fmt.Errorf("nothing matches %q; GGUF files in the repo: %s", sel, strings.Join(names, ", "))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Missing reports split parts of a multi-file GGUF the selection lacks.
func Missing(sel []File) []string {
	have := map[string]bool{}
	for _, f := range sel {
		have[f.Path] = true
	}
	var miss []string
	for _, f := range sel {
		m := splitRE.FindStringSubmatch(f.Path)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2]) // the regex admits only digits
		stem := strings.TrimSuffix(f.Path, m[0])
		for i := 1; i <= n; i++ {
			p := fmt.Sprintf("%s-%05d-of-%s.gguf", stem, i, m[2])
			if !have[p] {
				miss = append(miss, p)
				have[p] = true
			}
		}
	}
	return miss
}

// Progress hears bytes as they land: done of total for one file.
type Progress func(file string, done, total int64)

// ErrIntegrity marks a file whose size or sha256 does not match the Hub's.
var ErrIntegrity = errors.New("integrity check failed")

// Download fetches f from repo@commit into dir, resuming dir/<path>.part,
// and renames it into place only after the size and the sha256 match.
// A file already in place with the right size and hash is skipped.
func (c *Client) Download(ctx context.Context, repo, commit string, f File, dir string, prog Progress) (skipped bool, err error) {
	dst := filepath.Join(dir, filepath.FromSlash(f.Path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if st, err := os.Stat(dst); err == nil && st.Size() == f.Size {
		if f.SHA256 == "" {
			return true, nil
		}
		if sum, err := manifest.HashFile(dst, nil); err == nil && sum == f.SHA256 {
			return true, nil
		}
	}
	part := dst + ".part"
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		lastErr = c.fetch(ctx, repo, commit, f, part, prog)
		if lastErr == nil || errors.Is(lastErr, ErrIntegrity) || ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, os.Rename(part, dst)
}

// fetch appends to part from its current size, then checks the whole file.
func (c *Client) fetch(ctx context.Context, repo, commit string, f File, part string, prog Progress) error {
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	st, err := out.Stat()
	if err != nil {
		return err
	}
	have := st.Size()
	if have > f.Size {
		// A part longer than the file is from something else: start over.
		if err := out.Truncate(0); err != nil {
			return err
		}
		have = 0
	}
	if have < f.Size {
		u := fmt.Sprintf("%s/%s/resolve/%s/%s", c.base(), repo, commit, escapePath(f.Path))
		hdr := map[string]string{}
		if have > 0 {
			hdr["Range"] = fmt.Sprintf("bytes=%d-", have)
		}
		resp, err := c.get(ctx, u, hdr)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := status(resp, repo+"/"+f.Path); err != nil {
			return err
		}
		if have > 0 && resp.StatusCode != http.StatusPartialContent {
			// The server ignored the range: what follows is the whole file.
			if err := out.Truncate(0); err != nil {
				return err
			}
			have = 0
		}
		w := &counter{w: out, n: have, total: f.Size, file: f.Path, prog: prog}
		if _, err := io.Copy(w, resp.Body); err != nil {
			return err
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if st, err := os.Stat(part); err != nil || st.Size() != f.Size {
		got := int64(-1)
		if st != nil {
			got = st.Size()
		}
		_ = os.Remove(part)
		return fmt.Errorf("%w: %s is %d bytes, the Hub says %d", ErrIntegrity, f.Path, got, f.Size)
	}
	if f.SHA256 != "" {
		sum, err := manifest.HashFile(part, nil)
		if err != nil {
			return err
		}
		if sum != f.SHA256 {
			_ = os.Remove(part)
			return fmt.Errorf("%w: %s sha256 %s, the Hub says %s", ErrIntegrity, f.Path, sum, f.SHA256)
		}
	}
	return nil
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

type counter struct {
	w     io.Writer
	n     int64
	total int64
	file  string
	prog  Progress
	last  time.Time
}

func (c *counter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	if c.prog != nil && (time.Since(c.last) > 500*time.Millisecond || c.n == c.total) {
		c.last = time.Now()
		c.prog(c.file, c.n, c.total)
	}
	return n, err
}
