// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

// berth update: whether a newer berth release exists, and, on the
// operator's say-so, the release's verified binary in place of this one.
// The command reaches GitHub because the operator typed it; with
// [update] check = false Berth on its own never does. Berth never
// restarts anything after the replace.

import (
	"bufio"
	"context"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/T-Crypt/berth/internal/update"
)

func runUpdate(c *Context) int {
	fs := c.FlagSet()
	check := fs.Bool("check", false, "say whether a newer release exists, and stop there")
	yes := fs.Bool("yes", false, "replace the binary without asking")
	force := fs.Bool("force", false, "replace a dev build with the newest release too")
	if code, done := c.Parse(fs); done {
		return code
	}
	if code, done := c.noArgs(); done {
		return code
	}
	// The check is one 10 s GET; the rest is downloading this machine's
	// binary, which may take a while on a slow line.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	res, err := update.Refresh(ctx, defaultStateFile("update.json"), c.Version)
	if err != nil {
		return c.fail(Systemf("the update check could not reach GitHub: %v", err).
			WithHint("check your connection and run it again; nothing was changed"))
	}
	if !*check {
		return runUpdateReplace(c, ctx, res, *yes, *force)
	}
	if !res.Newer {
		c.Printf("%s %s is the newest release\n", c.Style.OK(markOK), res.Latest)
		return ExitOK
	}
	c.Printf("%s %s (this binary: %s)\n", c.Style.Warn("[NEW]"), res.Latest, c.Version)
	c.Bullet("release: %s", res.URL)
	c.Bullet("run `berth update` to download and replace this binary")
	return ExitOK
}

// currentExecutable is the seam a test replaces, so a replace lands on a
// throwaway file and never on the running test binary.
var currentExecutable = os.Executable

// assetName is the release's file for this machine. The release workflow
// publishes berth-linux-amd64 and berth-linux-arm64; a Windows release
// would add berth-windows-<arch>.exe.
func assetName() string {
	name := "berth-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func runUpdateReplace(c *Context, ctx context.Context, res update.Result, yes, force bool) int {
	if c.Version == "dev" && !force {
		return c.fail(Failf("this is a dev build; berth update replaces release binaries").
			WithHint("pass --force to replace a dev build with the newest release anyway"))
	}
	if c.Version != "dev" && !res.Newer {
		c.Printf("%s %s is the newest release\n", c.Style.OK(markOK), res.Latest)
		return ExitOK
	}
	exec, err := currentExecutable()
	if err != nil {
		return c.fail(Systemf("find the running binary: %v", err))
	}
	name := assetName()
	down, ok := res.Assets[name]
	if !ok {
		return c.fail(Systemf("the %s release publishes no %s binary", res.Latest, name).
			WithHint("build it instead: go build -o berth ./cmd/berth"))
	}
	sums, ok := res.Assets["SHA256SUMS"]
	if !ok {
		return c.fail(Systemf("the %s release publishes no SHA256SUMS", res.Latest))
	}

	newPath := exec + ".new"
	defer os.Remove(newPath) // nothing survives a refusal or a failure
	if err := update.Download(ctx, down, newPath); err != nil {
		return c.fail(Systemf("download the %s binary: %v", res.Latest, err))
	}
	sumsPath := defaultStateFile("SHA256SUMS")
	if err := update.Download(ctx, sums, sumsPath); err != nil {
		return c.fail(Systemf("download SHA256SUMS: %v", err))
	}
	b, err := os.ReadFile(sumsPath)
	if err != nil {
		return c.fail(Systemf("read the downloaded sums: %v", err))
	}
	if err := update.Verify(b, name, newPath); err != nil {
		return c.fail(Systemf("the download refused itself: %v", err).
			WithHint("its sha256 does not match the release's SHA256SUMS; nothing was replaced"))
	}

	if !yes {
		if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
			return c.fail(Failf("no terminal to confirm the replace on").
				WithHint("rerun with --yes to replace without asking"))
		}
		c.Printf("replace %s with %s? [y/N] ", exec, res.Latest)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			c.Printf("%s not replaced\n", c.Style.Dim("[skip]"))
			return ExitOK
		}
	}
	if err := update.ReplaceBinary(newPath, exec); err != nil {
		return c.fail(Systemf("replace %s: %v", exec, err).
			WithHint("the old binary is still in place; nothing was restarted"))
	}
	c.Printf("%s %s is in place of %s\n", c.Style.OK(markOK), res.Latest, c.Version)
	c.Bullet("installed with `berth service install`: systemctl --user restart berthd, plus berth-gateway and berth-harbor where running (on Windows, re-run the tasks it created)")
	c.Bullet("run in a terminal: start the command again, the new binary takes over from the next process")
	c.Bullet("berth restarts nothing itself")
	return ExitOK
}
