package suite

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/T-Crypt/berth/internal/config"
)

// Engine is what the daemon, gateway and engine readers see for one suite
// install: a comfyui engine on the install's port, whose start line is the
// exact `berth run` shape the operator's unit already uses. The derived
// engine is read-only state: the suite never writes it back to berth.toml.
func Engine(i Install) config.Engine {
	url := fmt.Sprintf("http://127.0.0.1:%d", i.Port)
	py := "python3"
	if i.Venv != "" {
		py = venvPython(i.Path, i.Venv)
	}
	start := []string{berthBinary(), "run", "--as", i.Name, "--priority", i.Priority, "--drain-comfyui", url}
	if i.KeepUp {
		start = append(start, "--keep-comfyui-up")
	}
	// The flag names the model-paths file explicitly, so the wiring holds
	// even on a ComfyUI old enough not to load the default file name; the
	// loader skips a path it already registered, so loading it twice is a
	// no-op.
	start = append(start, "--", py, "main.py", "--listen", "127.0.0.1", "--port", strconv.Itoa(i.Port),
		"--extra-model-paths-config", filepath.Join(i.Path, "extra_model_paths.yaml"))
	return config.Engine{
		Name:         i.Name,
		Type:         "comfyui",
		Kind:         "image",
		URL:          url,
		GUI:          url,
		Priority:     i.Priority,
		KeepComfyUI:  i.KeepUp,
		DrainComfyUI: url,
		Start:        start,
	}
}

// berthBinary is the berth the derived start line runs: this binary when it
// can be named (the gateway is the berth binary), "berth" on PATH when it
// cannot.
func berthBinary() string {
	if p, err := os.Executable(); err == nil && p != "" {
		return p
	}
	return "berth"
}
