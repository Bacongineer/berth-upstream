// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/T-Crypt/berth/internal/update"
)

// releaseServer serves one GitHub release, the API answer and both
// assets, from one httptest server; checks counts the API hits it got.
// A nil sums means the sums file carries a hash the binary does not
// match.
// writeAll is the test server's write: it reports, it does not panic.
func writeAll(t *testing.T, w http.ResponseWriter, b []byte) {
	t.Helper()
	if _, err := w.Write(b); err != nil {
		t.Errorf("test server: %v", err)
	}
}

func releaseServer(t *testing.T, tag, binary string, sums string) *httptest.Server {
	t.Helper()
	if sums == "" {
		sums = "0000000000000000000000000000000000000000000000000000000000000000  " + assetName() + "\n"
	}
	var srv *httptest.Server
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			writeAll(t, w, []byte(fmt.Sprintf(
				`{"tag_name":"%s","html_url":"https://github.com/T-Crypt/berth/releases/tag/%s",`+
					`"assets":[{"name":"%s","browser_download_url":"%s/asset"},`+
					`{"name":"SHA256SUMS","browser_download_url":"%s/sums"}]}`,
				tag, tag, assetName(), srv.URL, srv.URL)))
		case "/asset":
			writeAll(t, w, []byte(binary))
		case "/sums":
			writeAll(t, w, []byte(sums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func correctSums(binary string) string {
	sum := sha256.Sum256([]byte(binary))
	return hex.EncodeToString(sum[:]) + "  " + assetName() + "\n"
}

// runUpdateCLI runs one `berth update` invocation in process with the
// version a release build would carry.
func runUpdateCLI(t *testing.T, version string, args ...string) (int, string, string) {
	t.Helper()
	isolate(t)
	var out, errOut bytes.Buffer
	code := Run(Env{Args: append([]string{"update"}, args...), Out: &out, Err: &errOut, Version: version})
	return code, out.String(), errOut.String()
}

// stdInPipe turns the process' stdin into a pipe: the no-terminal refusal
// is the same in CI and at a person's shell, and a test can never block
// on a prompt.
func stdInPipe(t *testing.T) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
		w.Close()
	})
}

func TestUpdateCheckReportsNewer(t *testing.T) {
	srv := releaseServer(t, "v0.3.1", "the binary", "")
	prev := update.APIURL
	update.APIURL = srv.URL + "/api"
	t.Cleanup(func() { update.APIURL = prev })

	code, out, _ := runUpdateCLI(t, "0.3.0", "--check")
	if code != 0 {
		t.Fatalf("a newer release is not a failure: %d\n%s", code, out)
	}
	if !strings.Contains(out, "v0.3.1") || !strings.Contains(out, "release:") {
		t.Errorf("the check says what is newer and where it lives:\n%s", out)
	}
}

func TestUpdateCheckUpToDate(t *testing.T) {
	srv := releaseServer(t, "v0.3.1", "the binary", "")
	prev := update.APIURL
	update.APIURL = srv.URL + "/api"
	t.Cleanup(func() { update.APIURL = prev })

	code, out, _ := runUpdateCLI(t, "0.3.1", "--check")
	if code != 0 {
		t.Fatalf("being current is not a failure: %d\n%s", code, out)
	}
	if !strings.Contains(out, "newest release") {
		t.Errorf("an up-to-date check names the newest release:\n%s", out)
	}
}

func TestUpdateRefusesDevBuild(t *testing.T) {
	srv := releaseServer(t, "v0.3.1", "the binary", "")
	prev := update.APIURL
	update.APIURL = srv.URL + "/api"
	t.Cleanup(func() { update.APIURL = prev })

	code, _, errOut := runUpdateCLI(t, "dev")
	if code != 1 {
		t.Fatalf("a dev build is refused, code %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "dev build") {
		t.Errorf("the refusal names the dev build:\n%s", errOut)
	}
}

func TestUpdateReplacesTheBinary(t *testing.T) {
	srv := releaseServer(t, "v0.3.1", "the new binary", correctSums("the new binary"))
	prev := update.APIURL
	update.APIURL = srv.URL + "/api"
	t.Cleanup(func() { update.APIURL = prev })

	home := isolate(t)
	target := filepath.Join(home, assetName())
	if err := os.WriteFile(target, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevExe := currentExecutable
	currentExecutable = func() (string, error) { return target, nil }
	t.Cleanup(func() { currentExecutable = prevExe })

	code, out, errOut := runUpdateCLIWith(t, "0.3.0", "--yes")
	if code != 0 {
		t.Fatalf("the replace fails: %d\n%s%s", code, out, errOut)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "the new binary" {
		t.Errorf("the target holds %q after the replace", b)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Errorf("the downloaded file survives the replace: %v", err)
	}
	if !strings.Contains(out, "restart") {
		t.Errorf("the output names what to restart:\n%s", out)
	}
}

func TestUpdateChecksumMismatchRefuses(t *testing.T) {
	srv := releaseServer(t, "v0.3.1", "the binary", "") // sums say otherwise
	prev := update.APIURL
	update.APIURL = srv.URL + "/api"
	t.Cleanup(func() { update.APIURL = prev })

	home := isolate(t)
	target := filepath.Join(home, assetName())
	if err := os.WriteFile(target, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevExe := currentExecutable
	currentExecutable = func() (string, error) { return target, nil }
	t.Cleanup(func() { currentExecutable = prevExe })

	code, _, errOut := runUpdateCLIWith(t, "0.3.0", "--yes")
	if code != 2 {
		t.Fatalf("a mismatched hash is a system failure, code %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "refused") {
		t.Errorf("the refusal is named:\n%s", errOut)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "the old binary" {
		t.Errorf("the old binary moved despite the mismatch: %q", b)
	}
}

func TestUpdateWithoutYesNeedsATerminal(t *testing.T) {
	srv := releaseServer(t, "v0.3.1", "the binary", correctSums("the binary"))
	prev := update.APIURL
	update.APIURL = srv.URL + "/api"
	t.Cleanup(func() { update.APIURL = prev })
	stdInPipe(t)

	home := isolate(t)
	target := filepath.Join(home, assetName())
	if err := os.WriteFile(target, []byte("the old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevExe := currentExecutable
	currentExecutable = func() (string, error) { return target, nil }
	t.Cleanup(func() { currentExecutable = prevExe })

	code, _, errOut := runUpdateCLIWith(t, "0.3.0")
	if code != 1 {
		t.Fatalf("no terminal means no replace, code %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "terminal") {
		t.Errorf("the refusal names the missing terminal:\n%s", errOut)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "the old binary" {
		t.Errorf("the old binary moved without a confirmation: %q", b)
	}
}

// runUpdateCLIWith is runUpdateCLI without its own isolate, so a test
// that set the home first keeps it.
func runUpdateCLIWith(t *testing.T, version string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(Env{Args: append([]string{"update"}, args...), Out: &out, Err: &errOut, Version: version})
	return code, out.String(), errOut.String()
}
