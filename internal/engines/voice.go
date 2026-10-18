package engines

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/T-Crypt/berth/internal/proto"
)

// kindOf is what an engine type produces; custom engines carry theirs in
// the config, so a voice server Berth has no reader for is still tracked as one.
func kindOf(typ, configured string) string {
	switch typ {
	case "comfyui":
		return proto.KindImage
	case "kokoro":
		return proto.KindTTS
	case "custom":
		if configured != "" {
			return configured
		}
	}
	return proto.KindLLM
}

// kokoro reads Kokoro-FastAPI (text to speech, default :8880): /health
// answers {"status":"healthy"}, which is also its fingerprint for discovery;
// then the OpenAI-style model list and the voice count.
func (w *Watcher) kokoro(ctx context.Context, base, keyEnv string, out *proto.Engine) error {
	b, err := w.getKey(ctx, base+"/health", keyEnv)
	if err != nil {
		return err
	}
	var h struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(b, &h) != nil || h.Status != "healthy" {
		return fmt.Errorf("/health: not a Kokoro-FastAPI answer")
	}
	if err := w.openAIModels(ctx, base, keyEnv, out); err != nil {
		return err
	}
	if b, err := w.getKey(ctx, base+"/v1/audio/voices", keyEnv); err == nil {
		var v struct {
			Voices  []json.RawMessage `json:"voices"`
			Default string            `json:"default_voice"`
		}
		if json.Unmarshal(b, &v) == nil {
			out.Voices = len(v.Voices)
			if v.Default != "" {
				out.Models = append(out.Models, proto.Model{Name: "voice " + strings.TrimSpace(v.Default) + " (default)", State: "loaded"})
			}
		}
	}
	return nil
}
