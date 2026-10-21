// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// A start spends time waiting for the card: the holder still has to drain
// while the disk sits idle. This file uses that wait on the engine's own
// files, asking the kernel to read them into its page cache so the engine's
// loader reads RAM instead of the disk. The kernel runs the reads on its own
// schedule and takes the pages back when the machine needs the RAM, so a
// warm file is cached, never reserved.

package gateway

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/pagecache"
)

// prefetch asks the kernel to read the engine's files into the page cache.
// It is all the engine's `prefetch` config does: no part of a start waits
// for it, and no part of a start fails because of it. A file that cannot be
// asked for is named in a WARN and passed over: a model the operator moved
// is one line in the journal, not a reason to refuse a request.
func (g *Gateway) prefetch(e *config.Engine) {
	files, bytes := prefetchFiles(e.Prefetch)
	if len(files) == 0 {
		return
	}
	g.log().Info("gateway prefetching engine files", "engine", e.Name, "files", len(files), "bytes", bytes)
	ask := g.prefetcher()
	for _, f := range files {
		err := ask(f)
		if err == nil {
			continue
		}
		if errors.Is(err, pagecache.ErrUnsupported) {
			// A box with no kernel page-cache interface says so for every
			// file: a fact about the platform, not a mistake to fix.
			g.log().Debug("no page-cache interface", "engine", e.Name, "err", err)
			return
		}
		g.log().Warn("gateway prefetch failed", "engine", e.Name, "path", f, "err", err)
	}
}

// prefetcher is the ask itself; nil means the kernel's pagecache.Prefetch.
func (g *Gateway) prefetcher() func(path string) error {
	if g.Prefetch != nil {
		return g.Prefetch
	}
	return pagecache.Prefetch
}

// prefetchFiles is what one prefetch covers: every configured path, with a
// directory standing for the regular files directly inside it. Nothing is
// walked deeper than that: a subdirectory holds files the entry did not name.
func prefetchFiles(paths []string) (files []string, bytes uint64) {
	for _, p := range paths {
		fi, err := os.Stat(p)
		switch {
		case err != nil:
			// Pass the path on: the ask looks it up again and reports
			// whatever it finds there.
			files = append(files, p)
		case !fi.IsDir():
			files = append(files, p)
			bytes += uint64(fi.Size())
		default:
			es, err := os.ReadDir(p)
			if err != nil {
				files = append(files, p)
				continue
			}
			for _, ent := range es {
				child := filepath.Join(p, ent.Name())
				// os.Stat, not the directory entry: a models directory of
				// links into a blob store is a normal layout, and a link to
				// a regular file is a file. A link to a directory stays
				// below the no-deeper rule.
				fi, err := os.Stat(child)
				if err != nil || !fi.Mode().IsRegular() {
					continue
				}
				files = append(files, child)
				bytes += uint64(fi.Size())
			}
		}
	}
	return files, bytes
}
