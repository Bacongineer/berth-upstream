package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ModelPathKeys are the model folders ComfyUI can load through
// extra_model_paths. The same roots are written under every key on purpose:
// ComfyUI filters each key by the file extensions that key loads, so chat
// GGUFs in a shared root never surface as image models.
var ModelPathKeys = []string{
	"checkpoints", "diffusion_models", "loras", "vae", "text_encoders",
	"clip", "clip_vision", "embeddings", "controlnet", "upscale_models",
}

const modelPathsHeader = `# Written by Berth. The roots are the node's model roots from berth.toml
# ([models] dirs, [library] roots and the usual stores). To change them,
# edit berth.toml and restart the daemon or run berth engine install again.`

// RenderModelPaths renders the content of extra_model_paths.yaml for roots:
// the deduped absolute roots that exist, under every model-folder key. A
// missing root is dropped, as models.DefaultRoots drops a missing store.
func RenderModelPaths(roots []string) string {
	seen := map[string]bool{}
	var abs []string
	for _, r := range roots {
		a, err := filepath.Abs(r)
		if err != nil || seen[a] {
			continue
		}
		seen[a] = true
		if fi, err := os.Stat(a); err == nil && fi.IsDir() {
			abs = append(abs, a)
		}
	}
	var b strings.Builder
	b.WriteString(modelPathsHeader + "\n")
	if len(abs) == 0 {
		b.WriteString("berth: {}\n")
		return b.String()
	}
	b.WriteString("berth:\n")
	for _, k := range ModelPathKeys {
		fmt.Fprintf(&b, "  %s: |\n", k)
		for _, r := range abs {
			b.WriteString("    " + r + "\n")
		}
	}
	return b.String()
}

// WriteModelPaths writes <install>/extra_model_paths.yaml, the file ComfyUI
// loads at start for the node's model roots.
func WriteModelPaths(inst Install, roots []string) error {
	if err := os.MkdirAll(inst.Path, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(inst.Path, "extra_model_paths.yaml"), []byte(RenderModelPaths(roots)), 0o644)
}
