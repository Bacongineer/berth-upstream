// Copyright 2026 Trevin Tindall (T-Crypt)
// SPDX-License-Identifier: Apache-2.0

// Package gateway is `berth gateway`: one local port in front of every
// engine on the node. A request names a model; the gateway finds the engine
// that serves it, starts that engine under a lease if it is not running
// (draining the holder like any other request), waits until it answers, and
// proxies the request to it, streaming. Engines keep their own ports, and
// stay leased when called directly.
//
// The gateway can load models, so every request must carry the node's API
// key as a bearer token, name a loopback Host (DNS rebinding) and come from no
// browser origin (a page cannot forge the key, and is refused before it is
// even asked for).
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/T-Crypt/berth/internal/apikey"
	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/host"
	"github.com/T-Crypt/berth/internal/proto"
	"github.com/T-Crypt/berth/internal/runner"
	"github.com/T-Crypt/berth/internal/server"
)

type Gateway struct {
	Socket  string          // the node's berthd socket
	Engines []config.Engine // configured engines; those with Start can be started on demand
	Ready   time.Duration   // how long a start may wait for a berth and then to answer; 0 = 10 min
	Key     string          // the node's API key; required on every request
	// AllowHosts are extra Host names the loopback gateway answers to: the name a
	// same-machine reverse proxy (tailscale serve) forwards. The listener stays on
	// loopback and the key and Origin checks still apply.
	AllowHosts []string
	Log        *slog.Logger
	Client     *http.Client
	// HeadroomFloor is the RAM kept for the rest of the machine on a cold
	// start; 0 = defaultHeadroomFloor.
	HeadroomFloor uint64
	// MemAvailable is RAM available in bytes plus whether the box reports
	// it; nil reads the live machine. Tests inject a fake here.
	MemAvailable func() (avail uint64, ok bool)
	// StateFile records the engines this gateway started as their own
	// processes, so the next gateway adopts the ones still running
	// instead of starting them again. Empty disables the record.
	StateFile string
	// LogMax caps an engine's log file before it starts over; 0 = 64 MiB.
	LogMax int64

	mu      sync.Mutex
	running map[string]*started // engines this gateway started, by name
	failed  map[string]*failure // engines whose last start died before answering
	lists   map[string]cached   // engine URL -> its /v1/models, briefly cached
	wakes   map[string]*wake    // the wake page's last start, by engine name

	adoptOnce sync.Once
	stateMu   sync.Mutex
	records   map[string]engineRec // the run processes still going, by engine name

	logOnce sync.Once
	logDir  string
	logErr  error // one engineLogs() decision per gateway
}

type started struct {
	exited   chan struct{}
	code     int
	stderr   *lockedBuf // berth run's own lines: granted, waiting, draining
	output   *tailBuf   // the tail of the engine's own stdout and stderr
	answered bool       // the engine answered at least once (under g.mu)
	why      *startError
}

// failure is an engine whose start died before it answered. A start that
// fails the same way every time (a bad config, a card too full for it) is
// not run again on every client retry: for a while the gateway answers with
// the cause at once instead of paying another cold start to learn it again.
type failure struct {
	err   *startError
	n     int       // failed starts in a row
	until time.Time // no new start before this
}

// startCooldown is the pause after the first failed start; each failure in
// a row doubles it, up to maxStartCooldown. A start that answers clears it.
const (
	startCooldown    = 15 * time.Second
	maxStartCooldown = 5 * time.Minute
)

func cooldown(n int) time.Duration {
	d := startCooldown
	for i := 1; i < n && d < maxStartCooldown; i++ {
		d *= 2
	}
	return min(d, maxStartCooldown)
}

// defaultHeadroomFloor is the RAM the gateway keeps for the rest of the
// machine when an engine cold-starts: a start that would leave less than
// this available is refused instead of risking system-oomd. Tunable per
// gateway via HeadroomFloor.
const defaultHeadroomFloor = 2 << 30

// guardDecision is what the cold-start headroom guard decided, and why.
// The handler only maps allow/deny to HTTP; the reason lives here.
type guardDecision struct {
	allow bool
	code  string // set on deny, e.g. "no_headroom"
	msg   string
	avail uint64 // RAM available when decided
	need  uint64 // engine estimate plus the floor
}

// decideGuard is pure: deny when starting an engine estimated at estimate
// would leave less than floor available. A zero estimate (an engine with
// no configured footprint) allows: refusing on unknown data strands users.
func decideGuard(avail, floor, estimate uint64) guardDecision {
	if estimate == 0 {
		return guardDecision{allow: true, avail: avail}
	}
	need := estimate + floor
	if avail < need {
		return guardDecision{
			allow: false,
			code:  "no_headroom",
			msg: fmt.Sprintf("only %s RAM available, the engine needs %s plus a %s floor",
				server.MiB(avail), server.MiB(estimate), server.MiB(floor)),
			avail: avail,
			need:  need,
		}
	}
	return guardDecision{allow: true, avail: avail, need: need}
}

type cached struct {
	at     time.Time
	models []string
}

func (g *Gateway) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (g *Gateway) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// Handler serves the OpenAI-compatible surface plus the ComfyUI
// passthrough: /prompt queues a workflow (starting ComfyUI under a lease
// when it is down), while /ws /queue /history /view /upload proxy to a
// running ComfyUI.
func (g *Gateway) Handler() http.Handler {
	g.adoptOnce.Do(g.adopt)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", g.models)
	mux.HandleFunc("GET /v1/berth/wait", g.wait)
	for _, p := range []string{"/v1/chat/completions", "/v1/completions", "/v1/embeddings"} {
		mux.HandleFunc("POST "+p, g.forward)
	}
	mux.HandleFunc("POST /prompt", g.comfyPrompt)
	mux.HandleFunc("/ws", g.comfyProxy)
	mux.HandleFunc("/queue", g.comfyProxy)
	mux.HandleFunc("/history/", g.comfyProxy)
	mux.HandleFunc("/view", g.comfyProxy)
	mux.HandleFunc("/upload/", g.comfyProxy)
	return g.guard(mux)
}

// guard admits a request only with the key, a loopback Host and no Origin.
// OpenAI clients send no Origin; browsers always do on a cross-site POST.
func (g *Gateway) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !loopbackHost(r.Host) && !g.allowedHost(r.Host):
			apiError(w, http.StatusForbidden, "call the gateway at 127.0.0.1 or localhost")
		case r.Header.Get("Origin") != "":
			apiError(w, http.StatusForbidden, "the gateway does not answer browser pages")
		case !apikey.Bearer(r.Header.Get("Authorization"), g.Key):
			w.Header().Set("WWW-Authenticate", `Bearer realm="berth"`)
			apiError(w, http.StatusUnauthorized, "send the node's API key as a bearer token (`berth key` prints it)")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// allowedHost reports whether host, port stripped, is one of AllowHosts.
func (g *Gateway) allowedHost(host string) bool {
	name := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		name = hh
	}
	for _, a := range g.AllowHosts {
		if a != "" && strings.EqualFold(name, a) {
			return true
		}
	}
	return false
}

func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// target is where a model's requests go, and how to start it if it is down.
type target struct {
	url    string
	engine *config.Engine // set when the gateway may start it
	live   bool           // an engine already answering lists the model
}

// live is every engine the node reads that is up, configured or discovered.
func (g *Gateway) live() []proto.Engine {
	if st := g.nodeStatus(); st != nil {
		return st.Engines
	}
	return nil
}

// nodeStatus is berthd's whole view, or nil when it does not answer.
func (g *Gateway) nodeStatus() *proto.Status {
	conn, raw, err := server.Dial(g.Socket)
	if err != nil {
		return nil
	}
	defer raw.Close()
	if conn.Send(proto.Msg{Op: proto.OpStatus}) != nil {
		return nil
	}
	m, err := conn.Recv()
	if err != nil {
		return nil
	}
	return m.Status
}

// listed is what an engine's own /v1/models says it can serve. llama-swap and
// Ollama list every model they can load, not only the loaded ones, which is
// what lets them answer for a cold model themselves.
func (g *Gateway) listed(ctx context.Context, base string) []string {
	g.mu.Lock()
	if c, ok := g.lists[base]; ok && time.Since(c.at) < 30*time.Second {
		g.mu.Unlock()
		return c.models
	}
	g.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/models", nil)
	if err != nil {
		return nil
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var r struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&r) != nil {
		return nil
	}
	out := make([]string, 0, len(r.Data))
	for _, d := range r.Data {
		out = append(out, d.ID)
	}
	g.mu.Lock()
	if g.lists == nil {
		g.lists = map[string]cached{}
	}
	g.lists[base] = cached{time.Now(), out}
	g.mu.Unlock()
	return out
}

// resolve finds the engine for model: an engine that is up and lists it
// first (it loads the model itself, under its own leases), else a configured
// engine that serves it and can be started.
func (g *Gateway) resolve(ctx context.Context, model string) (target, bool) {
	st := g.nodeStatus()
	for _, e := range g.live() {
		// A parked engine answers its list with the weights in RAM; the
		// resume, not the list, says when it may be proxied.
		if e.Up && !parkedIn(st, e.Name) && slices.Contains(g.listed(ctx, e.URL), model) {
			return target{url: e.URL, live: true}, true
		}
	}
	for i := range g.Engines {
		e := &g.Engines[i]
		if slices.Contains(e.Serves, model) {
			return target{url: e.URL, engine: e}, true
		}
	}
	return target{}, false
}

func (g *Gateway) catalog(ctx context.Context) []string {
	seen := map[string]bool{}
	for _, e := range g.live() {
		if e.Up {
			for _, m := range g.listed(ctx, e.URL) {
				seen[m] = true
			}
		}
	}
	for _, e := range g.Engines {
		for _, m := range e.Serves {
			seen[m] = true
		}
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func (g *Gateway) models(w http.ResponseWriter, r *http.Request) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list", Data: []model{}}
	for _, m := range g.catalog(r.Context()) {
		out.Data = append(out.Data, model{ID: m, Object: "model", OwnedBy: "berth"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func apiError(w http.ResponseWriter, code int, msg string) {
	apiErrorCode(w, code, "", msg)
}

// apiErrorCode is an OpenAI-shaped error with a reason code a client can
// branch on; a 503 also says when to try again (30 s unless the caller
// already set a time it knows).
func apiErrorCode(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	if status == http.StatusServiceUnavailable && w.Header().Get("Retry-After") == "" {
		w.Header().Set("Retry-After", "30")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(berthError(code, msg))
}

// berthError is the OpenAI-shaped error body, with the reason code when
// there is one. The JSON path encodes it; the streaming path sends it as
// one SSE event.
func berthError(code, msg string) map[string]any {
	e := map[string]any{"message": msg, "type": "berth"}
	if code != "" {
		e["code"] = code
	}
	return map[string]any{"error": e}
}

// startError is why an engine could not be brought up for a request.
type startError struct {
	code  string // no_start, outranked, drained_while_starting, no_berth, start_failed, engine_oom, not_ready, no_headroom
	msg   string
	retry time.Duration // when set, the client is told to try again after this (Retry-After)
}

func (e *startError) Error() string { return e.msg }

// classify reads why a start ended before its engine answered: the lease
// outcomes from what the in-process berth run printed (stderr), a crash from
// the engine's own output, where the cause is.
func classify(name string, exit int, stderr, output string) *startError {
	last := lastLine(stderr)
	switch {
	case strings.Contains(stderr, "juggle:"):
		return &startError{code: "outranked", msg: fmt.Sprintf("%s lost a juggle for the card: %s", name, last)}
	case strings.Contains(stderr, "berth: draining"):
		return &startError{code: "drained_while_starting", msg: fmt.Sprintf("%s was drained before it answered: %s", name, last)}
	case exit == runner.ExitRefused:
		return &startError{code: "no_berth", msg: fmt.Sprintf("%s got no berth: %s", name, last)}
	}
	cause := causeLine(output)
	if cause == "" {
		cause = last
	}
	if oomLine.MatchString(output) {
		return &startError{code: "engine_oom", msg: fmt.Sprintf("%s ran out of GPU memory while starting (exit %d): %s", name, exit, cause)}
	}
	return &startError{code: "start_failed", msg: fmt.Sprintf("%s did not start (exit %d): %s", name, exit, cause)}
}

// errLine marks a line of engine output that reads as a failure; oomLine one
// that names a GPU allocation failure, in the words the common engines use
// (CUDA and PyTorch, cuBLAS handle creation, llama.cpp, Vulkan, ROCm).
var (
	errLine = regexp.MustCompile(`(?i)error|fail|fatal|panic|exception|traceback|abort|denied|cannot|unable|no such|not found|out of memory|status [1-9]`)
	oomLine = regexp.MustCompile(`(?i)out of memory|CUDA_ERROR_OUT_OF_MEMORY|cudaErrorMemoryAllocation|OutOfMemoryError|CUBLAS_STATUS_ALLOC_FAILED|cublasCreate\S*:? cuBLAS status [13]\b|failed to allocate .*buffer|ErrorOutOfDeviceMemory|hipErrorOutOfMemory`)
)

// causeLine is the last line of the engine's output that reads as a
// failure, else its last line; empty when it printed nothing. Long lines
// are cut so an error body stays readable.
func causeLine(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	pick := ""
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if pick == "" {
			pick = l
		}
		if errLine.MatchString(l) {
			pick = l
			break
		}
	}
	if len(pick) > 300 {
		pick = pick[:300] + "..."
	}
	return pick
}

// forward reads the model from the body, makes sure its engine is up (under
// a lease), then proxies the request unchanged. A streaming request whose
// engine is down gets the SSE preamble first, so the wait for the cold
// start carries keep-alive comments and holds clients with idle timeouts.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		apiError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &req) != nil || req.Model == "" {
		apiError(w, http.StatusBadRequest, "the request needs a model")
		return
	}
	t, ok := g.resolve(r.Context(), req.Model)
	if !ok {
		apiError(w, http.StatusNotFound, fmt.Sprintf("no engine on this node serves %q (GET /v1/models lists what does)", req.Model))
		return
	}
	preamble := false
	if !t.live {
		if req.Stream {
			preamble = true
			ssePreamble(w)
			if err := g.keepAlive(w, r, t.engine); err != nil {
				g.sseFailure(w, t.engine.Name, req.Model, err)
				return
			}
		} else if err := g.ensure(r.Context(), t.engine); err != nil {
			g.startFailed(w, t.engine.Name, req.Model, err)
			return
		}
	}
	u, err := url.Parse(t.url)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "engine url: "+err.Error())
		return
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.FlushInterval = -1 // stream tokens as the engine sends them
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Host = u.Host
	if preamble {
		p.ServeHTTP(bodyOnly{w}, r) // the SSE preamble owns the headers
		return
	}
	p.ServeHTTP(w, r)
}

// keepAliveEvery is how often a streaming request that waits for its
// engine gets an SSE comment. A var so tests need not wait 10 s.
var keepAliveEvery = 10 * time.Second

// ssePreamble sends a streaming response's headers before the engine is
// up, so the keep-alive comments can start while it loads.
func ssePreamble(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()
}

// keepAlive runs ensure for a streaming request, writing an SSE comment
// every keepAliveEvery with the seconds gone, so proxies and clients hold
// the connection and a reader sees the wait moving.
func (g *Gateway) keepAlive(w http.ResponseWriter, r *http.Request, e *config.Engine) error {
	type outcome struct{ err error }
	ch := make(chan outcome, 1)
	go func() { ch <- outcome{g.ensure(r.Context(), e)} }()
	start := time.Now()
	tick := time.NewTicker(keepAliveEvery)
	defer tick.Stop()
	for {
		select {
		case o := <-ch:
			return o.err
		case <-r.Context().Done():
			return r.Context().Err()
		case <-tick.C:
			fmt.Fprintf(w, ": berth: loading %s, %d s\n\n", e.Name, int64(time.Since(start).Seconds()))
			_ = http.NewResponseController(w).Flush()
		}
	}
}

// sseFailure answers a streaming request whose engine would not come up:
// the same OpenAI-shaped error the 503 path carries, as one SSE event.
func (g *Gateway) sseFailure(w http.ResponseWriter, engine, what string, err error) {
	code := ""
	var se *startError
	if errors.As(err, &se) {
		code = se.code
	}
	g.log().Warn("gateway start failed", "engine", engine, "request", what, "code", code, "err", err)
	b, _ := json.Marshal(berthError(code, err.Error()))
	fmt.Fprintf(w, "data: %s\ndata: [DONE]\n\n", b)
	_ = http.NewResponseController(w).Flush()
}

// bodyOnly is a ResponseWriter whose headers are already on the wire: the
// SSE preamble went out while the engine loaded. The engine's body streams
// through and header changes are dropped.
type bodyOnly struct{ http.ResponseWriter }

func (bodyOnly) WriteHeader(int)               {}
func (b bodyOnly) Flush()                      { _ = http.NewResponseController(b.ResponseWriter).Flush() }
func (b bodyOnly) Unwrap() http.ResponseWriter { return b.ResponseWriter }

// wait blocks until the engine for the model named in the query answers,
// for harnesses that pre-warm before their first real request. A failed
// start is the same 503 the request path answers with.
func (g *Gateway) wait(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	if model == "" {
		apiError(w, http.StatusBadRequest, "name the model to wait for: /v1/berth/wait?model=NAME")
		return
	}
	t, ok := g.resolve(r.Context(), model)
	if !ok {
		apiError(w, http.StatusNotFound, fmt.Sprintf("no engine on this node serves %q (GET /v1/models lists what does)", model))
		return
	}
	if !t.live {
		if err := g.ensure(r.Context(), t.engine); err != nil {
			g.startFailed(w, t.engine.Name, model, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "ready": true})
}

// startFailed answers a request whose engine would not come up: a 503
// carrying the reason code a client can branch on.
func (g *Gateway) startFailed(w http.ResponseWriter, engine, what string, err error) {
	code := ""
	var se *startError
	if errors.As(err, &se) {
		code = se.code
	}
	g.log().Warn("gateway start failed", "engine", engine, "request", what, "code", code, "err", err)
	if se != nil && se.retry > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((se.retry+time.Second-1)/time.Second)))
	}
	apiErrorCode(w, http.StatusServiceUnavailable, code, err.Error())
}

// comfyTarget finds ComfyUI: a live one first (it takes the prompt
// itself), else a configured one the gateway may start. A parked ComfyUI
// is not the live one: the prompt must resume it first.
func (g *Gateway) comfyTarget(ctx context.Context) (target, bool) {
	st := g.nodeStatus()
	for _, e := range g.live() {
		if e.Up && e.Type == "comfyui" && !parkedIn(st, e.Name) && g.comfyAnswers(ctx, e.URL) {
			return target{url: e.URL, live: true}, true
		}
	}
	for i := range g.Engines {
		e := &g.Engines[i]
		if e.Type == "comfyui" {
			return target{url: e.URL, engine: e}, true
		}
	}
	return target{}, false
}

// comfyAnswers is true when ComfyUI's queue endpoint responds. The
// /v1/models probe the chat path uses does not exist on ComfyUI.
func (g *Gateway) comfyAnswers(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/queue", nil)
	if err != nil {
		return false
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// comfyPrompt queues a workflow: it makes sure ComfyUI is up under a lease
// (draining the holder like any other request, holding this request until
// the engine answers), then proxies the prompt unchanged.
func (g *Gateway) comfyPrompt(w http.ResponseWriter, r *http.Request) {
	t, ok := g.comfyTarget(r.Context())
	if !ok {
		apiError(w, http.StatusNotFound, "no ComfyUI engine on this node (configure an engine of type comfyui)")
		return
	}
	if !t.live {
		if err := g.ensure(r.Context(), t.engine); err != nil {
			g.startFailed(w, t.engine.Name, "prompt", err)
			return
		}
	}
	g.comfyProxyTo(t.url, w, r)
}

// comfyProxy passes the rest of the ComfyUI surface (/ws, /history, /view,
// /upload, /queue) to the engine unchanged, streaming. /prompt is the entry
// point that starts a down engine; these answer 503 naming it instead, so
// a thumbnail fetch never cold-starts a 12 GB server by accident.
func (g *Gateway) comfyProxy(w http.ResponseWriter, r *http.Request) {
	t, ok := g.comfyTarget(r.Context())
	if !ok {
		apiError(w, http.StatusNotFound, "no ComfyUI engine on this node (configure an engine of type comfyui)")
		return
	}
	if !t.live && !g.comfyAnswers(r.Context(), t.url) {
		apiError(w, http.StatusServiceUnavailable, "ComfyUI is not running; POST /prompt first to start it")
		return
	}
	g.comfyProxyTo(t.url, w, r)
}

func (g *Gateway) comfyProxyTo(base string, w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(base)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "engine url: "+err.Error())
		return
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.FlushInterval = -1 // stream progress as ComfyUI sends it
	r.Host = u.Host
	p.ServeHTTP(w, r)
}

// ensure starts e under a lease unless this gateway already has, then waits
// until it answers. A start the broker refuses comes back as the reason.
func (g *Gateway) ensure(ctx context.Context, e *config.Engine) error {
	// ComfyUI answers no /v1/models; its queue endpoint is the liveness
	// probe instead, so a cold start waits for the real server.
	probe := g.answers
	if e.Type == "comfyui" {
		probe = g.comfyAnswers
	}
	// A parked engine answers while the weights sit in RAM: the resume
	// goes before any probe, or the request is proxied to RAM.
	if parks(e) {
		switch err := g.resume(ctx, e); {
		case err == nil:
			return nil
		case !errors.Is(err, errNotParked):
			return err
		}
	}
	if probe(ctx, e.URL) {
		return nil // already up (started by hand or by its own manager)
	}
	ready := g.ready()
	g.mu.Lock()
	if g.running == nil {
		g.running = map[string]*started{}
	}
	s, ok := g.running[e.Name]
	if !ok {
		if len(e.Start) == 0 {
			g.mu.Unlock()
			return &startError{code: "no_start", msg: fmt.Sprintf("%s is not answering at %s and has no start command in berth.toml", e.Name, e.URL)}
		}
		if err := g.checkHeadroom(e); err != nil {
			g.mu.Unlock()
			g.log().Warn("gateway start refused", "engine", e.Name, "err", err)
			return err
		}
		if f := g.failed[e.Name]; f != nil {
			if left := time.Until(f.until); left > 0 {
				g.mu.Unlock()
				return &startError{code: f.err.code, retry: left,
					msg: fmt.Sprintf("%s; %d start(s) in a row failed, the next is allowed in %s",
						f.err.msg, f.n, left.Round(time.Second))}
			}
		}
		s = &started{exited: make(chan struct{}), stderr: &lockedBuf{}, output: &tailBuf{}}
		g.running[e.Name] = s
		go g.run(e, s, ready)
	}
	g.mu.Unlock()

	deadline := time.Now().Add(ready)
	for {
		if probe(ctx, e.URL) {
			g.mu.Lock()
			s.answered = true
			delete(g.failed, e.Name)
			g.mu.Unlock()
			return nil
		}
		select {
		case <-s.exited:
			return s.why
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return &startError{code: "not_ready", msg: fmt.Sprintf("%s did not answer within %s", e.Name, ready)}
		}
	}
}

func (g *Gateway) ready() time.Duration {
	if g.Ready > 0 {
		return g.Ready
	}
	return 10 * time.Minute
}

// checkHeadroom refuses a cold start that would leave the machine without
// its RAM floor. It never caps, throttles or nices an engine: it only
// refuses to start one. Unknown footprints (no estimate) and boxes that do
// not report memory fail open toward starting.
func (g *Gateway) checkHeadroom(e *config.Engine) error {
	estimate := e.NeedsRAMMiB << 20
	if estimate == 0 {
		return nil
	}
	avail, ok := g.memAvailable()
	if !ok {
		return nil
	}
	d := decideGuard(avail, g.headroomFloor(), estimate)
	if !d.allow {
		return &startError{code: d.code, msg: d.msg}
	}
	return nil
}

func (g *Gateway) headroomFloor() uint64 {
	if g.HeadroomFloor > 0 {
		return g.HeadroomFloor
	}
	return defaultHeadroomFloor
}

func (g *Gateway) memAvailable() (uint64, bool) {
	if g.MemAvailable != nil {
		return g.MemAvailable()
	}
	s := new(host.Reader).Read()
	if !s.Supported {
		return 0, false
	}
	return s.RAMTotal - s.RAMUsed, true
}

// run starts e as its own `berth run` process: under the user manager's
// scope (or a new session), so a gateway restart or crash leaves the
// engine up. The process takes the lease and runs the drain ladder
// exactly as any other berth run; the gateway reads its output for the
// tail a failed start quotes, and records it, so the next gateway
// adopts the engine instead of starting it again. The start waits for
// the berth no longer than the request that asked for it, so a start
// the client gave up on cannot fire hours later and take the card.
func (g *Gateway) run(e *config.Engine, s *started, wait time.Duration) {
	o := runner.OptionsFor(e, runner.Over{Socket: g.Socket, Group: e.Name, Wait: wait})
	code, err := g.spawnRun(e.Name, s, o)
	if err != nil {
		g.log().Error("gateway could not start the engine process", "engine", e.Name, "err", err)
		s.why = &startError{code: "start_failed", msg: fmt.Sprintf("%s could not be started: %v", e.Name, err)}
	} else {
		s.code = code
		g.log().Info("gateway engine exited", "engine", e.Name, "code", code)
	}
	g.mu.Lock()
	delete(g.running, e.Name)
	if !s.answered {
		if s.why == nil {
			s.why = classify(e.Name, s.code, s.stderr.String(), s.output.String())
		}
		// Only a crash cools down: a lost juggle, a drain or a refused
		// lease is the broker's answer, and the next try may well win.
		if s.why.code == "start_failed" || s.why.code == "engine_oom" {
			if g.failed == nil {
				g.failed = map[string]*failure{}
			}
			f := g.failed[e.Name]
			if f == nil {
				f = &failure{}
				g.failed[e.Name] = f
			}
			f.n++
			f.err, f.until = s.why, time.Now().Add(cooldown(f.n))
			g.log().Warn("gateway start cooling down", "engine", e.Name, "failed_in_a_row", f.n,
				"next_start_in", cooldown(f.n), "cause", s.why.msg)
		}
	}
	g.mu.Unlock()
	close(s.exited)
}

// answers is true when the engine's model list responds.
func (g *Gateway) answers(ctx context.Context, base string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/v1/models", nil)
	if err != nil {
		return false
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// tailBuf keeps the last tailMax bytes written to it. An engine can print
// for days (Strata logs every token batch); only the end says why it died.
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

const tailMax = 16 << 10

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - tailMax; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
