package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/T-Crypt/berth/internal/config"
)

// The wake page. A browser cannot use the gateway API (it needs the key and
// refuses any Origin), so a second loopback listener serves one page per
// engine with a gui URL in berth.toml: GET /gui/<name> sends the browser to
// the engine's web UI when it answers, or shows a page that asks for a start
// (under a lease, like any gateway start) and follows the engine there once
// it does. A bookmark or a home-page tile can point here instead of at an
// engine that may be stopped.
//
// It holds no key: it can only start an engine that has a start command and
// a gui URL, which is what the operator asked for by writing both. A start is
// a POST that must come from the page itself (same origin), so another site
// cannot make a visitor's browser drain the card.

//go:embed gui
var guiFS embed.FS

var guiPage = template.Must(template.ParseFS(guiFS, "gui/page.html"))

// wake is the last start the wake page asked for, per engine.
type wake struct {
	starting bool
	err      error
}

// GUIHandler serves the wake pages and their assets.
func (g *Gateway) GUIHandler() http.Handler {
	assets, _ := fs.Sub(guiFS, "gui")
	mux := http.NewServeMux()
	mux.Handle("GET /_wake/", http.StripPrefix("/_wake/", http.FileServerFS(assets)))
	mux.HandleFunc("GET /gui/{$}", g.guiIndex)
	mux.HandleFunc("GET /gui/{name}", g.guiShow)
	mux.HandleFunc("GET /gui/{name}/state", g.guiState)
	mux.HandleFunc("POST /gui/{name}/wake", g.guiWake)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/gui/", http.StatusFound)
	})
	return g.guiGuard(mux)
}

// guiGuard keeps the page on loopback (or a name the operator allowed for a
// same-machine proxy), loads nothing from elsewhere, and admits a POST only
// from this origin.
func (g *Gateway) guiGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		switch {
		case !loopbackHost(r.Host) && !g.allowedHost(r.Host):
			apiError(w, http.StatusForbidden, "open the wake page at 127.0.0.1 or localhost")
		case r.Method == http.MethodPost && !sameOrigin(r):
			apiError(w, http.StatusForbidden, "a start must come from the wake page itself")
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// sameOrigin is true when a browser says the request came from this origin.
// A client that sends neither header is not a browser and cannot be tricked
// into a cross-site request.
func sameOrigin(r *http.Request) bool {
	if s := r.Header.Get("Sec-Fetch-Site"); s != "" {
		return s == "same-origin"
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// guiEngine is the configured engine name, if it has a web UI to wake.
func (g *Gateway) guiEngine(name string) (*config.Engine, bool) {
	for i := range g.Engines {
		e := &g.Engines[i]
		if e.Name == name && e.GUI != "" && len(e.Start) > 0 {
			return e, true
		}
	}
	return nil, false
}

func (g *Gateway) probe(ctx context.Context, e *config.Engine) bool {
	if e.Type == "comfyui" {
		return g.comfyAnswers(ctx, e.URL)
	}
	return g.answers(ctx, e.URL)
}

type guiView struct {
	Title   string
	Engine  string // empty on the index
	Engines []string
}

func (g *Gateway) guiIndex(w http.ResponseWriter, _ *http.Request) {
	v := guiView{Title: "berth"}
	for _, e := range g.Engines {
		if e.GUI != "" && len(e.Start) > 0 {
			v.Engines = append(v.Engines, e.Name)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = guiPage.Execute(w, v)
}

// guiShow sends the browser on when the engine answers; otherwise it shows
// the page, whose script asks for the start.
func (g *Gateway) guiShow(w http.ResponseWriter, r *http.Request) {
	e, ok := g.guiEngine(r.PathValue("name"))
	if !ok {
		apiError(w, http.StatusNotFound, "no engine of that name has a gui and a start command in berth.toml")
		return
	}
	if g.probe(r.Context(), e) {
		http.Redirect(w, r, e.GUI, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = guiPage.Execute(w, guiView{Title: e.Name + " - starting", Engine: e.Name})
}

type guiStatus struct {
	State  string `json:"state"` // up, starting, down or failed
	Detail string `json:"detail,omitempty"`
	Code   string `json:"code,omitempty"` // a start's reason code when it failed
	URL    string `json:"url,omitempty"`  // where the browser goes once up
}

func (g *Gateway) guiState(w http.ResponseWriter, r *http.Request) {
	e, ok := g.guiEngine(r.PathValue("name"))
	if !ok {
		apiError(w, http.StatusNotFound, "no engine of that name has a gui and a start command in berth.toml")
		return
	}
	st := guiStatus{State: "down"}
	if g.probe(r.Context(), e) {
		st = guiStatus{State: "up", URL: e.GUI}
	} else {
		g.mu.Lock()
		wk := g.wakes[e.Name]
		if s, ok := g.running[e.Name]; ok {
			st.Detail = lastLine(s.stderr.String())
		}
		g.mu.Unlock()
		switch {
		case wk != nil && wk.starting:
			st.State = "starting"
		case wk != nil && wk.err != nil:
			st.State = "failed"
			st.Detail = wk.err.Error()
			if se, ok := wk.err.(*startError); ok {
				st.Code = se.code
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st)
}

// guiWake starts the engine in the background and answers at once; the page
// polls /state. A second ask while a start is under way joins it.
func (g *Gateway) guiWake(w http.ResponseWriter, r *http.Request) {
	e, ok := g.guiEngine(r.PathValue("name"))
	if !ok {
		apiError(w, http.StatusNotFound, "no engine of that name has a gui and a start command in berth.toml")
		return
	}
	g.mu.Lock()
	if g.wakes == nil {
		g.wakes = map[string]*wake{}
	}
	already := g.wakes[e.Name] != nil && g.wakes[e.Name].starting
	if !already {
		g.wakes[e.Name] = &wake{starting: true}
	}
	g.mu.Unlock()
	if !already {
		g.log().Info("wake page starting engine", "engine", e.Name)
		go func() {
			// The start outlives the page's request: a closed tab still
			// leaves the engine coming up, bounded by the gateway's ready.
			ctx, cancel := context.WithTimeout(context.Background(), g.ready())
			defer cancel()
			err := g.ensure(ctx, e)
			if err != nil {
				g.log().Warn("wake page start failed", "engine", e.Name, "err", err)
			}
			g.mu.Lock()
			g.wakes[e.Name] = &wake{err: err}
			g.mu.Unlock()
		}()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(guiStatus{State: "starting"})
}
