# Engines

| Engine | Reader (`type`) | Found on its own | What Berth reads |
|---|---|---|---|
| llama.cpp `llama-server` | `llamacpp` | :8080 | tokens, rates, queue, context window, KV slots |
| llama-swap | `llama-swap` | :8080 | every running model's upstream, request log, per-model window |
| Ollama | `ollama` | :11434 | loaded models and their VRAM |
| vLLM | `vllm` | :8000 | models, window; tokens and queue from `/metrics` |
| SGLang | `sglang` | :30000 | models, window; tokens and queue with `--enable-metrics` |
| ComfyUI | `comfyui` | :8188 | jobs running and queued, version; job-boundary handoff |
| Kokoro-FastAPI (text to speech) | `kokoro` | :8880 | health, models, voice count, default voice |
| LM Studio | `lmstudio` | :1234 | loaded models, context length |
| KoboldCpp | `koboldcpp` | :5001 | model, context length |
| TabbyAPI (ExLlamaV3) | `tabbyapi` | :5000 | models, context length |
| LocalAI | `localai` | :8080 | models, tokens |
| Unsloth Studio | `unsloth` | :8888 | health; models with an API key |
| FreeToken | `freetoken` | :1919 | tokens, queue |
| [NInfer](https://github.com/T-Crypt/ninfer-4090) | `ninfer` | as llama.cpp | as llama.cpp, plus draft acceptance |
| [Strata](https://github.com/T-Crypt/strata) | `strata` | :8080 | model, window, tokens, queue |
| Any OpenAI-compatible server | `openai` | :8080, :8000 | models |
| Anything else | `custom` | never | your health, models, metrics and slots paths; `kind = "tts"`, `"stt"`, `"image"` or `"other"` tracks a voice or image server as one |

Every reader takes any URL, so any port and any host works. Engines that need a key name the
environment variable that holds it (`api_key_env`); the key never goes in the config.
On Unix, `api_key_file` instead names an absolute, owner-private regular file containing
one token. Symlinks and files accessible to other users are rejected. The two sources
are mutually exclusive. `berth run --api-key-file` uses the same source for drain requests;
detached gateway launches pass the source reference, never its contents. Other platforms
use `api_key_env` and explicitly reject file credentials. An engine's figures are its
own claims and are shown apart from what the driver measures.

llama.cpp adds a request's tokens to its counters only when the request ends. While a
request runs, an engine card shows the decode and prefill rates its last finished request
measured, and 0 once the engine is idle.

## ComfyUI as a suite

`berth engine install|show|update|rollback|remove` manages ComfyUI as a suite: the core,
ComfyUI-Manager and every git-based node pack are separate components, each pinned to a git
commit with one previous commit kept as the rollback snapshot. Several installs run side by
side on separate ports; each install's derived engine appears in `berth engine check`,
`berth status` and the gateway's wake page, and its start line is the exact `berth run` shape
an operator's unit already uses (the only alignment needed is `--as <install-name>`).

- `install NAME --adopt PATH` records an existing checkout in place; `--path` clones a
  fresh one (PyTorch is your step: the install is registered either way, and ComfyUI's own
  start error names the missing piece).
- `update NAME` pins the core and each pack to a newer commit; `rollback NAME --component C`
  swaps back. A dirty component, or an install with jobs running, is refused: the update
  happens at the job boundary.
- `remove NAME` drops the state entry; `--delete --yes` also removes the directory.

The state lives in `engines.toml` beside `berth.toml`. Berth writes each install an
`extra_model_paths.yaml` naming the node's model roots from `berth.toml`, so an install's
models come from the same library the rest of the node uses; the daemon rewrites the file at
startup, so a change to the roots applies on the next restart.
