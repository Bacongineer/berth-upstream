package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/T-Crypt/berth/internal/config"
	"github.com/T-Crypt/berth/internal/proto"
)

// kokoroServer answers like Kokoro-FastAPI (api/src/main.py and
// routers/openai_compatible.py).
func kokoroServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"status": "healthy"}`)
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"tts-1","object":"model","owned_by":"kokoro"},{"id":"kokoro","object":"model","owned_by":"kokoro"}]}`)
		case "/v1/audio/voices":
			fmt.Fprint(w, `{"voices":[{"id":"af_heart","name":"af_heart"},{"id":"am_adam","name":"am_adam"},{"id":"ef_dora","name":"ef_dora"}],"default_voice":"af_heart"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestKokoroReader(t *testing.T) {
	srv := kokoroServer(t)
	w := &Watcher{Engines: []config.Engine{{Name: "tts", Type: "kokoro", URL: srv.URL}},
		Client: &http.Client{Timeout: 2 * time.Second}}
	w.PollOnce(context.Background())
	e := w.Snapshot()[0]
	if !e.Up || e.Kind != proto.KindTTS || e.Voices != 3 {
		t.Fatalf("kokoro = %+v", e)
	}
	if len(e.Models) != 3 || e.Models[2].Name != "voice af_heart (default)" {
		t.Fatalf("models = %+v", e.Models)
	}
	if !fingerprint(context.Background(), w.client(), srv.URL, "kokoro") {
		t.Fatal("fingerprint missed Kokoro")
	}
}

// A server with a plain healthy /health but no voice list is not Kokoro.
func TestKokoroFingerprintNeedsVoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"status":"healthy"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if fingerprint(context.Background(), &http.Client{Timeout: time.Second}, srv.URL, "kokoro") {
		t.Fatal("a bare healthy /health was taken for Kokoro")
	}
}

// A custom engine names its kind, so a voice server without a reader is
// still tracked as one; readers with a built-in kind keep theirs.
func TestEngineKinds(t *testing.T) {
	for _, c := range []struct{ typ, conf, want string }{
		{"custom", "stt", proto.KindSTT},
		{"custom", "", proto.KindLLM},
		{"comfyui", "tts", proto.KindImage},
		{"kokoro", "", proto.KindTTS},
		{"llamacpp", "", proto.KindLLM},
	} {
		if got := kindOf(c.typ, c.conf); got != c.want {
			t.Errorf("kindOf(%s, %q) = %s, want %s", c.typ, c.conf, got, c.want)
		}
	}
}
