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
| [Strata](https://github.com/T-Crypt/strata) | `strata` | :8080 | model from `/health`, window, tokens, queue, and its requests for the token ledger and leaderboard |
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

A Strata entry needs `type = "strata"`. The `llamacpp` reader names no model, so its requests never reach the
token ledger or the leaderboard. With `type = "strata"` each request lands under the model `/health` names, and two
Strata engines serving two quants get two rows on whatever port they run.

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

## Engines in a container or a Compose project

An engine in Docker or Podman is parented by containerd-shim, so the process tree cannot
reach its GPU processes. Name the container and Berth does the attribution instead:
`berth run --container NAME -- docker run --name NAME ...`, or `container = "NAME"` on the
engine's `[[engine]]` entry. The lease then owns every GPU process inside that container, and
a drain runs `docker stop` through the runtime CLI instead of signalling the wrapper.

An engine that is a Compose project names the project instead: `compose = "/srv/tagger/compose.yaml"`,
or `berth run --compose PATH`. The path is absolute, and an entry sets compose or container, never
both. Berth lists the project's containers with `compose -f PATH ps -q --all` on every poll and
names the whole set on the lease, so a second CUDA container of the same project is its engine's
memory rather than a stowaway, and a container that restarts under a new ID is picked up on the
next poll. A drain stops the project with `compose stop`, never `down`: `down` deletes containers
and networks you own. Your start command stays your own, and `docker compose -f PATH up` suits it:
it stays in the foreground while the project runs and exits after the stop. Put it in the entry's `start`, and
`berth run --as NAME` with no command after it runs that `start`, as the gateway does.

On a machine with more than one card, `gpu = "gpu1"` on the entry (or `berth run --gpu gpu1`) puts the
lease on that card, and `gpu = "gpu0,gpu1"` holds one lease across both. `berth run` sets `CUDA_VISIBLE_DEVICES` for the command it starts, which is the
`docker` CLI here, not the container. Give the container its card yourself with
`--gpus device=<UUID>`, or `device_ids` under the service's GPU reservation in Compose.

Check each service's restart policy. Docker restarts a container with `restart: always` the next time
its daemon starts, even after a drain stopped it, so a reboot or a Docker Desktop start puts it back on
the card without a lease. `berth run` reads the policy of each container it names on the lease and
warns about `always`. `unless-stopped` keeps a drained container stopped, but a container that was
running when the machine went down starts again at boot, also without a lease. With `restart: "no"`,
only your start command brings the project up.

A `batch` lease yields to a higher-priority request as soon as it asks, even seconds after its grant, so a
background project can be stopped right after it came up. `min_turn = "100s"` on the entry keeps the card for
that long after the grant. Only a hold (a game, `berth hold`) takes it out sooner. See [leasing](leasing.md).

Both forms work on Linux and Windows, and one runtime CLI does the work on either. Windows has one
limit you should plan around. The driver reports no per-container VRAM under WSL2, so a container's
memory arrives as the WSL VM's single process row. While a container engine is the only one on the
card, Berth counts that row as its lease, and the lease counts the larger of its `needs_mib` and the
row; with two container engines at once Berth cannot split one row between them, so each counts its
`needs_mib` and the row is reported once, as a stowaway.

Size `needs_mib` from your heaviest run, because a batch engine's footprint follows its batch. One
operator's photo tagger measured 2.1 GiB on a small scan and 6.7 GiB on its biggest, beside a hot
14.6 GiB model that gave way without an OOM; 2.1 GiB would have been the wrong number to declare.
[contrib/INTEGRATION.md](https://github.com/T-Crypt/berth/blob/main/contrib/INTEGRATION.md) has
the commands and the berth.toml shapes.
