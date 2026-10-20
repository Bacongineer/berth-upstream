// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package dl moves one file from a URL into place. A download that
// interrupted resumes where it stopped instead of starting over, and the
// file is accepted only after its size and, when the source publishes one,
// its sha256 match.
package dl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/T-Crypt/berth/internal/manifest"
)

// ErrIntegrity marks a file whose size or sha256 does not match what the
// source published. A caller that retries treats it differently from a
// network failure: the part is deleted, not appended to.
var ErrIntegrity = errors.New("integrity check failed")

// FetchSpec names one file to fetch and how to check it.
type FetchSpec struct {
	URL    string
	Part   string // the working file; an interrupted download resumes from it
	Dest   string // where the verified file lands
	Size   int64  // what the source states
	SHA256 string // the source's sha256; empty = the source publishes none
	// Header is set on every request, like a bearer token.
	Header map[string]string
	// Prog reports download progress, throttled to two calls a second.
	Prog func(done, total int64)
	// Label is how errors name the source.
	Label string
}

// Fetch runs one attempt: it appends to Part from where it already stands,
// checks size and hash, and renames the part to Dest when both agree. A
// part longer than the file is from something else and starts over.
func Fetch(ctx context.Context, hc *http.Client, spec FetchSpec) error {
	// No O_APPEND: Go opens an O_APPEND file on Windows without the write
	// access right, and a file without it cannot be truncated, which the
	// stale-part path below needs. The file has one writer, so a seek to
	// the end is the same append.
	out, err := os.OpenFile(spec.Part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	st, err := out.Stat()
	if err != nil {
		return err
	}
	have := st.Size()
	if have > spec.Size {
		if err := out.Truncate(0); err != nil {
			return err
		}
		have = 0
	}
	if _, err := out.Seek(have, io.SeekStart); err != nil {
		return err
	}
	if have < spec.Size {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
		if err != nil {
			return err
		}
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		req.Header.Set("User-Agent", "berth")
		for k, v := range spec.Header {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := status(resp, spec); err != nil {
			return err
		}
		if have > 0 && resp.StatusCode != http.StatusPartialContent {
			// The server ignored the range: what follows is the whole file.
			if err := out.Truncate(0); err != nil {
				return err
			}
			have = 0
			if _, err := out.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}
		w := &counter{w: out, n: have, total: spec.Size, prog: spec.Prog}
		if _, err := io.Copy(w, resp.Body); err != nil {
			return err
		}
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(spec.Part); err != nil || st.Size() != spec.Size {
		got := int64(-1)
		if st != nil {
			got = st.Size()
		}
		_ = os.Remove(spec.Part)
		return fmt.Errorf("%w: %s is %d bytes, %s says %d", ErrIntegrity, spec.Part, got, spec.Label, spec.Size)
	}
	if spec.SHA256 != "" {
		sum, err := manifest.HashFile(spec.Part, nil)
		if err != nil {
			return err
		}
		if sum != spec.SHA256 {
			_ = os.Remove(spec.Part)
			return fmt.Errorf("%w: %s sha256 %s, %s says %s", ErrIntegrity, spec.Part, sum, spec.Label, spec.SHA256)
		}
	}
	return os.Rename(spec.Part, spec.Dest)
}

func status(resp *http.Response, spec FetchSpec) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: %s: gated or private; accept its terms and set its token", spec.Label, resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("%s: %s: not found", spec.Label, resp.Status)
	}
	return fmt.Errorf("%s: answered %s", spec.Label, resp.Status)
}

// Retry runs Fetch with backoff between attempts, stopping on an integrity
// failure or when the context is done.
func Retry(ctx context.Context, hc *http.Client, spec FetchSpec, attempts int) error {
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		lastErr = Fetch(ctx, hc, spec)
		if lastErr == nil || errors.Is(lastErr, ErrIntegrity) || ctx.Err() != nil {
			break
		}
	}
	return lastErr
}

type counter struct {
	w     io.Writer
	n     int64
	total int64
	prog  func(done, total int64)
	last  time.Time
}

func (c *counter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	if c.prog != nil && (time.Since(c.last) > 500*time.Millisecond || c.n == c.total) {
		c.last = time.Now()
		c.prog(c.n, c.total)
	}
	return n, err
}
