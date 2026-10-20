// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package park moves an engine's weights to system RAM on a drain, so the
// next use resumes in seconds instead of reloading from disk. An Adapter
// names the calls that park and resume one engine: the built-in table
// covers ComfyUI, vLLM and SGLang, and a plain POST to a configured URL
// covers any other engine that can offload itself. Everything here talks
// to an engine's HTTP API; nothing in the package touches a GPU or a
// process.
package park

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/T-Crypt/berth/internal/comfy"
)

// Adapter is one engine's park and resume. Park moves the engine's weights
// out of VRAM; Resume puts them back. A nil field means that side has no
// call to make, and For only reports an adapter when Park is set.
type Adapter struct {
	Park   func(ctx context.Context, base string) error
	Resume func(ctx context.Context, base string) error
}

// For picks the park and resume calls for kind, one of config.Types: a
// non-empty URL wins over the built-in adapter for that side (a custom
// hook), and each unset side falls back to the table. The result is
// usable when it can park.
func For(kind, parkURL, resumeURL string) (Adapter, bool) {
	b, has := builtIn[kind]
	a := Adapter{}
	switch {
	case parkURL != "":
		a.Park = post(parkURL)
	case has:
		a.Park = b.Park
	}
	switch {
	case resumeURL != "":
		a.Resume = post(resumeURL)
	case has:
		a.Resume = b.Resume
	}
	return a, a.Park != nil
}

// BuiltInKinds are the engine kinds the table covers, sorted.
func BuiltInKinds() []string {
	out := make([]string, 0, len(builtIn))
	for k := range builtIn {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// builtIn is one entry per engine that can park itself through the API it
// already serves.
var builtIn = map[string]Adapter{
	// ComfyUI's /free with free_memory off moves the models to the CPU
	// offload device and keeps them there until free_memory or pressure,
	// so a park lands in RAM. Resume is a no-op: ComfyUI loads on the
	// next prompt it receives.
	"comfyui": {
		Park: func(ctx context.Context, base string) error {
			return comfy.Free(ctx, base, false)
		},
		Resume: func(context.Context, string) error { return nil },
	},
	// vLLM's sleep mode: level 1 offloads the weights to CPU RAM and
	// keeps the KV cache, which is the park this package needs. GET
	// /is_sleeping confirms it took. The server must start with
	// --enable-sleep-mode and VLLM_SERVER_DEV_MODE=1 for these
	// endpoints to exist.
	"vllm": {
		Park:   postPath("/sleep?level=1"),
		Resume: postPath("/wake_up"),
	},
	// SGLang releases its weights and KV cache to memory and takes them
	// back on resume; the server starts with --enable-memory-saver, and
	// both calls assert the engine is idle, so Berth makes them at a
	// slot boundary. A tags body would release or resume only some
	// regions; none is sent, so the whole engine parks.
	"sglang": {
		Park:   postPath("/release_memory_occupation"),
		Resume: postPath("/resume_memory_occupation"),
	},
}

// postPath is the built-in shape: a POST to base plus a fixed path, no
// body, a non-2xx answer as an error that names the endpoint.
func postPath(path string) func(context.Context, string) error {
	return func(ctx context.Context, base string) error {
		return postTo(ctx, strings.TrimRight(base, "/")+path)
	}
}

// post is the custom-hook shape: a plain POST to an absolute URL.
func post(url string) func(context.Context, string) error {
	return func(ctx context.Context, _ string) error {
		return postTo(ctx, url)
	}
}

func postTo(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("POST %s: %s", url, resp.Status)
	}
	return nil
}
