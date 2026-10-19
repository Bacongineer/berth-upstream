package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
)

// noFollow is a client that shows a redirect instead of following it.
var noFollow = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func guiReq(t *testing.T, method, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func guiState(t *testing.T, base, name string) guiStatus {
	t.Helper()
	resp := guiReq(t, "GET", base+"/gui/"+name+"/state", nil)
	defer resp.Body.Close()
	var s guiStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A visit to a stopped engine's wake page shows the page and takes nothing;
// the page's Start takes a lease, the state turns up, and the next visit goes straight to the
// engine's web UI. No API key is involved.
func TestWakePageStartsAndRedirects(t *testing.T) {
	sock, b := daemon(t)
	addr := freeAddr(t)
	t.Setenv("BERTH_GW_COMFY", addr)
	const gui = "https://box.example.ts.net:8188/"
	g := &Gateway{Socket: sock, Ready: 10 * time.Second, Key: testKey, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engines: []config.Engine{{Name: "comfyui", Type: "comfyui", URL: "http://" + addr,
			Start: []string{os.Args[0]}, Priority: "interactive", GUI: gui}}}
	srv := httptest.NewServer(g.GUIHandler())
	defer srv.Close()

	resp := guiReq(t, "GET", srv.URL+"/gui/comfyui", nil)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), `data-engine="comfyui"`) {
		t.Fatalf("wake page %d: %s", resp.StatusCode, page)
	}
	if s := guiState(t, srv.URL, "comfyui"); s.State != "offline" || !strings.Contains(s.Message, "the card is free") {
		t.Fatalf("state before a start = %+v", s)
	}
	if st := b.Status(); len(st.Leases) != 0 {
		t.Fatalf("opening the page took a lease: %+v", st.Leases)
	}

	resp = guiReq(t, "POST", srv.URL+"/gui/comfyui/wake", map[string]string{"Sec-Fetch-Site": "same-origin"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("wake = %d", resp.StatusCode)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		s := guiState(t, srv.URL, "comfyui")
		if s.State == "up" {
			if s.URL != gui {
				t.Fatalf("up state url = %q", s.URL)
			}
			break
		}
		if s.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("engine never came up: %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st := b.Status(); len(st.Leases) != 1 || st.Leases[0].Engine != "comfyui" {
		t.Fatalf("leases = %+v", st.Leases)
	}

	resp = guiReq(t, "GET", srv.URL+"/gui/comfyui", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != gui {
		t.Fatalf("visit when up = %d to %q, want 302 to %s", resp.StatusCode, resp.Header.Get("Location"), gui)
	}
}

// Another site cannot make a visitor's browser start an engine, a rebinding
// name is refused, and only engines with both gui and start are reachable.
func TestWakePageGuard(t *testing.T) {
	sock, b := daemon(t)
	g := &Gateway{Socket: sock, Ready: time.Second, Key: testKey, AllowHosts: []string{"box.example.ts.net"},
		Engines: []config.Engine{
			{Name: "comfyui", Type: "comfyui", URL: "http://127.0.0.1:1", Start: []string{"false"}, GUI: "http://127.0.0.1:1/"},
			{Name: "nogui", Type: "llamacpp", URL: "http://127.0.0.1:1", Start: []string{"false"}, Serves: []string{"m"}},
		}}
	srv := httptest.NewServer(g.GUIHandler())
	defer srv.Close()

	for _, c := range []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   int
	}{
		{"cross-site fetch", "POST", "/gui/comfyui/wake", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"foreign origin", "POST", "/gui/comfyui/wake", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"rebinding host", "GET", "/gui/comfyui", map[string]string{"Host": "evil.example"}, http.StatusForbidden},
		{"no gui", "GET", "/gui/nogui", nil, http.StatusNotFound},
		{"unknown", "POST", "/gui/nope/wake", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusNotFound},
		{"allowed proxy host", "GET", "/gui/", map[string]string{"Host": "box.example.ts.net:9796"}, http.StatusOK},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		for k, v := range c.hdr {
			if k == "Host" {
				req.Host = v
				continue
			}
			req.Header.Set(k, v)
		}
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if st := b.Status(); len(st.Leases) != 0 {
		t.Fatalf("a refused request took a lease: %+v", st.Leases)
	}

	// A start that fails reports its reason to the page.
	resp := guiReq(t, "POST", srv.URL+"/gui/comfyui/wake", map[string]string{"Origin": srv.URL})
	resp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s := guiState(t, srv.URL, "comfyui")
		if s.State == "failed" {
			if s.Code == "" || s.Detail == "" {
				t.Fatalf("failed state carries no reason: %+v", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed start never reported: %+v", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The page names who holds the card and what a start waits for, with
// llama-swap's models under their family.
func TestCardView(t *testing.T) {
	ninfer := proto.Lease{Engine: "NInfer-27B", Group: "llama-swap", State: "active"}
	starting := &wake{starting: true}
	for _, c := range []struct {
		name  string
		node  *proto.Status
		wk    *wake
		state string
		want  string
	}{
		{"free", &proto.Status{}, nil, "offline", "the card is free"},
		{"in use", &proto.Status{Leases: []proto.Lease{ninfer}}, nil, "offline", "in use by llama-swap (NInfer-27B). Start asks it to release"},
		{"pinned", &proto.Status{Leases: []proto.Lease{{Engine: "blender", State: "active", Pinned: true}}}, nil, "offline", "Berth never stops it"},
		{"no berthd", nil, nil, "offline", "berthd is not answering"},
		{"draining", &proto.Status{Leases: []proto.Lease{{Engine: "strata", State: "draining"}}}, starting, "waiting", "Waiting for strata release: finishing its current request"},
		{"releasing", &proto.Status{Leases: []proto.Lease{{Engine: "NInfer-27B", Group: "llama-swap", State: "releasing"}}}, starting, "waiting", "Waiting for llama-swap (NInfer-27B) release: freeing its memory"},
		{"granted", &proto.Status{Leases: []proto.Lease{{Engine: "comfyui", State: "active"}}}, starting, "starting", "loading comfyui"},
		{"failed", &proto.Status{}, &wake{err: &startError{"no_berth", "comfyui got no berth"}}, "failed", "did not start"},
	} {
		got := cardView("comfyui", c.node, c.wk)
		if got.State != c.state || !strings.Contains(got.Message, c.want) {
			t.Errorf("%s: %s %q, want %s containing %q", c.name, got.State, got.Message, c.state, c.want)
		}
	}
}
