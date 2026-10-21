<p align="center"><img src="assets/banner.svg" alt="berth: a llama harbor master on a night pier moors a padlock lantern to a dock board of GPUs. On gpu0 llama-swap hands the card to strata when its reply ends, ComfyUI waits for its job to finish on gpu1, and a game holds gpu2. Zero collisions." width="100%"></p>

<p align="center">
  <a href="https://github.com/T-Crypt/berth/releases"><img src="https://img.shields.io/github/v/release/T-Crypt/berth?color=7ad4f2&amp;labelColor=0a1017" alt="latest release"></a>
  <a href="https://github.com/T-Crypt/berth/actions/workflows/ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/T-Crypt/berth/ci.yml?branch=main&amp;label=ci&amp;labelColor=0a1017" alt="CI status"></a>
  <img src="https://img.shields.io/badge/go-1.27-7ad4f2?labelColor=0a1017" alt="Go 1.27">
  <img src="https://img.shields.io/badge/runs%20on-linux%20%7C%20windows%20(experimental)-e8b56f?labelColor=0a1017" alt="Linux, Windows experimental">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-7ee8ac?labelColor=0a1017" alt="Apache-2.0"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> &nbsp;·&nbsp; <a href="#how-it-works">How it works</a> &nbsp;·&nbsp; <a href="#install">Install</a> &nbsp;·&nbsp; <a href="#gateway-preview">Gateway</a> &nbsp;·&nbsp; <a href="#visibility">Harbor</a> &nbsp;·&nbsp; <a href="#roadmap">Roadmap</a>
</p>

# Berth

Berth makes the programs on your PC take turns with the GPU's memory, so they stop crashing each
other, and tells you who is holding it.

Berth decides who gets your GPU. Chat models, image pipelines, voice engines, games and GPU apps
lease the card instead of grabbing it, so two of them never load at once and crash each other
with an out-of-memory error. One Go binary, no cloud account, nothing else to install.

```sh
berth daemon &                                        # one broker per card
berth run --as llama -- llama-server -m model.gguf    # this engine now takes turns
berth status                                          # who holds the card, who waits, and why
```

## How it works

<p align="center"><img src="assets/how-it-works.svg" alt="How berth works. Four clients ask for the card: a chat agent through the gateway, a ComfyUI workflow, an engine under berth run, and a game. berthd queues them by priority, drains the holder when its reply or job ends, waits for the driver to show the memory free, then grants the lease. Below, a timeline of one 24 GiB card: llama-swap, strata, a game and ComfyUI take turns and never overlap; markers 1 to 4 show one handoff." width="100%"></p>

Every engine asks berthd for the card: through `berth run`, the gateway, ComfyUI's API or a game
claim. When another engine holds it, Berth:

1. queues the request by priority: `hold`, `foreground`, `interactive`, `batch`;
2. lets the holder finish its reply or image job, then unloads it;
3. waits until the driver shows the memory free;
4. grants the lease and starts the next engine, and the request that waited streams.

The numbered markers on the timeline are those four steps. A process that holds VRAM without a lease
shows up as a stowaway: Berth reports its PID and memory and leaves it running.

> **Status: alpha, v0.3.3.** Runs daily on an RTX 4090 workstation beside llama-swap, NInfer,
> Strata, ComfyUI and games, and testers run it on Windows. [Capabilities](#capabilities) lists what works and has tests; the
> [Roadmap](#roadmap) lists the phases and what is still missing.

### Berth and llama-swap

Use both. llama-swap swaps the models in its own config, and does it well. Berth governs the
whole card: llama-swap's models, ComfyUI, Ollama, vLLM, games, and any process that holds VRAM,
including the ones neither tool started. With Berth beside it, llama-swap's next model waits
for ComfyUI or the engine before it to give the memory back, and when a crashed process still
holds part of the card, Berth names its PID and how much it holds, so you aren't hunting
through `nvidia-smi` while a load fails. `berth wire llama-swap` adds the lease to every model
in your config in one pass. An upstream integration with llama-swap is planned, so the wrapper
line goes away.

## What Berth does

**Shares one card between everything**
- **Engines take turns.** You start each one through `berth run`. When another needs the
  card, Berth waits for the holder's current reply to end, unloads it (its own unload, then
  SIGTERM, then SIGKILL) and lets the next one in only after the driver shows the memory free.
- **Priorities.** `foreground`, `interactive`, `batch`, and `hold` above them all. A lower
  request waits and gets a reason naming the holder, never an OOM crash.
- **No ping-pong.** When two clients ask for different engines at once, a ranking picks one
  and the other waits, so the card stays put.
- **ComfyUI at a job boundary.** Berth waits for the running image job to finish before the handoff.
- **A minimum turn.** `min_turn = "5m"` keeps an engine on the card for five minutes after it
  gets it, whatever asks next; only a hold takes it out sooner.
- **Engines in containers.** `berth run --container NAME -- docker run ...` puts a Docker or
  Podman engine under its lease, and the drain stops it through `docker stop`.
- **Survives restarts.** Upgrade or restart the daemon and your running engines stay up and
  re-attach. The gateway's engines survive it too: it starts each one as its own `berth run`
  process (in its own systemd scope on Linux) and adopts them again on its next start.

**Faster switches, and a way out** *(v0.3)*
- **Park in RAM.** An engine that can offload itself (ComfyUI, vLLM, SGLang, or any engine that
  serves the [park protocol](contrib/PARK-PROTOCOL.md)) moves its weights to system memory on a
  drain and resumes from there instead of reloading from disk. Berth parks only when the RAM it
  measures covers the engine plus a floor, and stops parked engines first when memory runs short.
- **A way out of every lease state.** `berth stop ENGINE` drains an engine on purpose,
  `berth resume ENGINE` puts a parked one back, and `berth reclaim` stops what a stuck release
  left behind, once you confirm.
- **A place in the queue.** Every wait reason starts with the waiter's place ("2nd in line
  (behind interactive comfyui)"), and `berth status` adds a start estimate from measured switches.

**Games and other apps**
- **Games take the card while you play.** Launch a Steam, Lutris or gamescope game on Linux,
  or a Steam, Epic, GOG, EA, Ubisoft, Xbox or Battle.net game on Windows, and Berth drains your
  AI engines until you quit.
- **Creative apps keep their memory.** Berth names what Blender, DaVinci Resolve, OBS,
  Kdenlive, Premiere and After Effects hold and counts it against the card, without a
  stowaway report.
- **Your own rules.** `[[claim]]` in `berth.toml` covers anything else: a training run, a
  render farm job, a cracking session, by name, command line or launcher.
- **`berth hold`** frees the card for a call or a game you start yourself. Ctrl-C gives it back.
- **`berth run --pinned`** for an app that must never be stopped mid-work.

**One API for your engines** *(preview)*
- **`berth gateway`**: one OpenAI-compatible port. Name a model and Berth starts its engine,
  unloads whatever holds the card, waits for the load and streams the answer.
- It proxies ComfyUI's workflow API too, so an image request queues behind the same lease.
- **Wake pages** (`--gui-listen`): a bookmark to a stopped engine's web UI shows who holds the
  card and a Start button, says what the start waits for, and opens the UI once it answers.
- Bearer key on every API route, loopback only, browser origins refused.

**Models**
- **`berth pull hf:org/repo:Q4_K_M`** downloads from Hugging Face, resumes where it stopped,
  and checks every file against the size and sha256 the Hub publishes before using it.
- **`berth pull civitai:VERSION_ID`** does the same from CivitAI, with the hash the site
  publishes, and **`berth models`** shelves what it finds as chat, image or audio so a
  machine reading a shared root knows what a folder is.
- **`berth storage`** shows each model root's filesystem, free space against its floor, and
  the other operating systems it is shared with.
- **`berth verify`** re-checks pulled models offline and names any file that changed.
- **`berth models`** lists every model on the machine (GGUF, safetensors, the Hugging Face
  cache, LM Studio and Ollama's stores) with quant, size, parameters, context and when each
  was last used, so you can see what earns its disk space.
- **`berth models export ollama`** turns a GGUF you already have into an Ollama model.

**Keeps itself current**
- **`berth update`** asks the release page whether a newer berth exists and,
  when one does, downloads this machine's binary and the release's SHA256SUMS,
  checks the hash, and replaces the running binary in place after you confirm.
  It then names what to restart; it never restarts anything itself.
- **Update check.** The harbor asks GitHub at most once every 24 h, one GET to
  the API's releases/latest, cached in the state directory, and shows "Update
  available" in its header when the answer is a newer release. `[update]
  check = false` in `berth.toml` turns the check off, and with it off Berth
  never contacts GitHub on its own. `berth update` and `berth update --check`
  ask GitHub every time you type them and never read the harbor's cached answer.

**Sees everything on the card**
- **Finds your engines.** Berth reads llama.cpp, llama-swap, Ollama, vLLM, SGLang, ComfyUI,
  Kokoro, LM Studio, KoboldCpp, TabbyAPI, LocalAI and more on their default ports, plus any
  OpenAI-compatible or custom server you name.
- **Stowaways.** Berth reports any process holding VRAM without a lease, with its PID, name
  and memory, and leaves it running.
- **Built-in telemetry.** Per-card memory, load, temperature, power and clocks; tokens per
  second, queue, latency percentiles, KV cache use, a token ledger and a per-model
  leaderboard; 30 days of history on disk.
- **The harbor web UI** (`berth harbor`): a departures board of leases, a collisions-avoided
  counter, charts and the event log. Read-only, loopback by default.
- **Alerts** to the desktop or a webhook, `/metrics` for Prometheus, and `berth export` for
  CSV or JSON.

**Built for agents and scripts**
- **`berth skill install`** teaches Claude Code, Codex, OpenCode and other agents to ask Berth
  for the card instead of killing GPU processes.
- Every reporting command takes `--format json|yaml`. All of them keep results on stdout and
  diagnostics on stderr, and exit 0, 1 or 2 by one rule.

**Runs on** Linux with an NVIDIA GPU (supported) and Windows 10/11 with an NVIDIA GPU
(experimental). Apache-2.0.

## Quick start

```sh
go install github.com/T-Crypt/berth/cmd/berth@latest      # or a release binary, see Install
berth service install --harbor                            # berthd and the harbor, as user services
berth status                                              # the card, leases, the engines and their estimated VRAM
berth run --as llama --priority foreground -- llama-server -m model.gguf --port 8080
```

On Linux the services are systemd user units; on Windows they are Task
Scheduler logon tasks that need no admin and run in your session, where
your GPU apps are. The harbor answers on http://127.0.0.1:9798/. `berth
service status` shows them and `berth service uninstall` removes them; the
manual steps in Install do the same by hand.

The harbor and gateway take their addresses from berth.toml when their
`--listen` flags are not given: `[harbor] listen` and `[gateway] listen`,
so a port change is a config edit and a restart of the service, not a
reinstall. On Windows, installing also starts the tasks it creates, and
each task appends its output to a log file under the state directory,
which `berth service status` prints.

## Preview

The harbor UI (basic mode, served from the built-in demo fixture - every number on the screen
comes from `demo.json`). The tide shows the card's memory by holder over six hours; the
hatched stretch is a stowaway the fixture replays: a model that ran without a lease after a
daemon restart, reported and left alone.

<p align="center"><img src="assets/preview-harbor.png" alt="Berth harbor: the CARD tab with ticket counter, request KPIs, the tide of card memory by holder, and the model timeline" width="900"></p>

The Engines tab (advanced mode): the engine cards with their learned footprints as
EST. VRAM, the library (shelf, quant, size, the cached tier, the serving engine),
decode throughput, the token ledger, the per-model leaderboard, storage and residency.

<p align="center"><img src="assets/preview-requests.png" alt="Berth harbor Engines tab, advanced mode: decode throughput per engine, the token ledger, the per-model leaderboard, storage and residency" width="900"></p>

On a phone, the harbor fits the screen: the tabs move to a bar at the bottom and the panels
stack. This is the CARD tab on an iPhone, read from a live node on the reference workstation.

<p align="center"><img src="assets/preview-mobile.png" alt="Berth harbor on an iPhone: the CARD tab with the ticket counter, collisions avoided, the collision tiles and the decode and TPOT KPIs, with CARD, ENGINES and ABOUT in a bottom bar" width="300"></p>

### The harbor on your iPhone's Home Screen

The harbor listens on loopback, so first give your phone a way to reach it. On a tailnet,
publish it from the Berth machine and pass the name it serves under to the harbor:

```sh
tailscale serve --bg --https=443 http://127.0.0.1:9798
berth harbor --allow-host <machine>.<tailnet>.ts.net
```

Then, on the iPhone:

1. Open `https://<machine>.<tailnet>.ts.net/` in Safari.
2. Tap Share, then **Add to Home Screen**.
3. Name it (`berth` works) and tap **Add**.

The Home Screen icon is Pilot, the harbor master llama, and the name under it is `berth`.
The page stays read-only. The harbor has no login, so publish it only on a tailnet or a home
network you trust.

## Install

Two ways: hand it to your coding agent, or do it yourself. Both end in the same place: the
daemon running as a user service, your engines wrapped in `berth run`, and the harbor UI on
<http://127.0.0.1:9798/>.

### Option 1: have your agent install it

Paste this into Claude Code, Codex, Cursor, OpenCode or any coding agent that can run shell
commands on the machine with the GPU:

```text
Install Berth (https://github.com/T-Crypt/berth), a VRAM lease broker for NVIDIA GPUs, on this
Linux machine, following its README and contrib/INTEGRATION.md. Steps:

1. Check prerequisites: Linux, an NVIDIA driver with `nvidia-smi` working, systemd user
   services. Stop and tell me if any is missing.
2. Download the latest release binary for this CPU (berth-linux-amd64 or berth-linux-arm64)
   and its SHA256SUMS from https://github.com/T-Crypt/berth/releases/latest, verify the
   checksum, and install it as ~/.local/bin/berth. (If Go 1.27+ is installed you may instead
   run: go install github.com/T-Crypt/berth/cmd/berth@latest.) Run `berth version`.
3. Run `berth service install --harbor`. It writes the systemd user units
   (the files in contrib/) with your binary's path in place of the one they
   name, and starts them; a unit file that already differs is kept as .bak
   (on Windows: a task whose XML differs). Confirm with `berth status`.
4. Find the inference engines on this machine (llama.cpp, llama-swap, Ollama, vLLM, SGLang,
   ComfyUI, LM Studio, KoboldCpp, TabbyAPI, LocalAI and so on): what runs them (systemd unit,
   llama-swap config, script) and on which port. Show me the list before changing anything.
5. For each engine I approve, wrap its start command in `berth run` as contrib/INTEGRATION.md
   shows (ComfyUI: contrib/comfyui.service with --drain-comfyui; Ollama: a systemd override of
   `ollama serve`). For llama-swap, wire the whole roster in one pass instead of one model at a
   time: `berth wire llama-swap --config <llama-swap config> --write` keeps a backup beside the
   file, keeps each model's original command, and `--check` exits 1 when a model is unwired.
   Ask me which engine wins the card (--priority foreground) and which waits (interactive or
   batch). Back up every file before you edit it and tell me how to undo each change.
6. Engines on non-default ports or other machines: add [[engine]] entries or
   [engines] discover_ports / discover_hosts to ~/.config/berth/berth.toml, then run
   `berth config check` and `berth engine check`.
7. Restart what you changed, then show me `berth status` with each engine leased and read,
   and the harbor at http://127.0.0.1:9798/. Do not expose the harbor beyond loopback unless
   I ask; it has no authentication.
```

The agent asks before it wraps anything, and every change it makes has an undo.

### Option 2: manual

**1. The binary.** Download `berth-linux-amd64` (or `-arm64`) and `SHA256SUMS` from the
[latest release](https://github.com/T-Crypt/berth/releases/latest):

```sh
cd /tmp
curl -fLO https://github.com/T-Crypt/berth/releases/latest/download/berth-linux-amd64
curl -fLO https://github.com/T-Crypt/berth/releases/latest/download/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
install -Dm755 berth-linux-amd64 ~/.local/bin/berth
berth version
```

Or build it: `go install github.com/T-Crypt/berth/cmd/berth@latest` (Go 1.27+, no C toolchain).

**2. The daemon and the UI** (one per machine). `berth service
install --harbor` does this on both platforms; by hand, on Linux, they are
systemd user units from `contrib/`:

```sh
mkdir -p ~/.config/systemd/user
cp contrib/berthd.service contrib/berth-harbor.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now berthd berth-harbor
berth status            # the card, leases, stowaways, the engines and their estimated VRAM
```

Open <http://127.0.0.1:9798/>. Engines on their default ports appear on their own.

**3. Wrap your engines** so they lease the card instead of grabbing it. The pattern:

```sh
berth run --as <name> --priority <batch|interactive|foreground> -- <the engine's command>
```

Per-engine recipes (llama-swap, llama.cpp, Ollama, ComfyUI, vLLM, SGLang, custom) are in
[contrib/INTEGRATION.md](contrib/INTEGRATION.md).

**4. Optional configuration** in `~/.config/berth/berth.toml`; validate with
`berth config check`:

```toml
[metrics]
listen = "127.0.0.1:9797"      # Prometheus text, loopback only

[harbor]
# listen = "127.0.0.1:9798"    # where `berth harbor` listens; a LAN address exposes the web UI (no auth)

[engines]
# discover = true              # default: probe the well-known engine ports every minute
# discover_ports = [9123]      # extra ports to probe
# discover_hosts = ["192.168.1.40"]

[[engine]]                     # anything discovery does not find, on any port
name = "llama-swap"
type = "llama-swap"
url  = "http://127.0.0.1:9090"

[alerts]
[alerts.queue]
warn = 4
crit = 10

[models]
# dirs = ["/mnt/d/models"]   # the roots berth models scans and pull writes into

[library]
# free_floor = 0.10          # keep this fraction of every root free; 0 turns it off

[[library.root]]             # what a root holds and shares
path   = "/mnt/d/models"
shelf  = "image"             # chat | image | audio; beats what the files say
shared = "windows"           # the other side of a shared root, shown by berth storage
```

**Uninstall:** `systemctl --user disable --now berthd berth-harbor`, remove the two unit
files and `~/.local/bin/berth`, and take `berth run ... --` off any engine command you
wrapped. History lives in `~/.local/state/berth/`.

---

## Engines

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

### ComfyUI as a suite

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

---

## Capabilities

### Leasing (the core)
- **Explicit leases.** Engines start through `berth run`, which waits for a berth, starts
  the engine in its own process group, and gives the berth back when the engine exits.
- **Priorities.** `foreground` > `interactive` > `batch`. An equal or higher request
  asks the holder to drain; a lower one waits and is refused with a reason naming the
  holder (exit 75), never an out-of-memory crash.
- **Handoff in steps.** A drain tries the engine's own unload first (for llama-swap, its
  unload URL), then SIGTERM to the process group, then SIGKILL, each after a grace period.
- **Measured release.** Berth grants the next engine only after the driver shows the old
  holder's processes gone, since an exited process can still hold memory.
- **Stuck releases are caught.** A released lease whose memory never frees turns
  *aground* and keeps blocking, with a reason naming the process and how much it holds.
- **A way out for a stuck release.** `berth reclaim [lease]` lists what the stuck lease
  recorded, PID, start time, memory and command, and anything on the card no lease
  recorded ("not Berth's", report only). With `--yes` it stops exactly the recorded
  ones, SIGTERM first and SIGKILL to the survivors after `--grace`, re-checking each
  PID and start time immediately before every signal.
- **Process identity is PID plus start time**, so a reused PID never inherits a lease.
- **Children stay on the lease.** Berth records every descendant of an engine's wrapper each
  tick, so a child that escapes its process group and reparents keeps the lease stuck under its
  own name instead of turning into a stowaway.
- **Whole-card or sized leases.** `--needs-mib N` lets engines that fit share a card.
- **Fail-open, never silent.** If the daemon is down, `berth run` still starts the engine
  but records it as an *unprotected run* that status shows (`--fail-closed` refuses).
- **Survives a daemon restart.** A running engine stays up when berthd restarts or is
  upgraded; `berth run` dials the new daemon and re-attaches by PID plus start time. For
  15 s after a start the daemon grants nothing new while such engines are still due back,
  so nothing is loaded onto a card an engine already holds.
- **ComfyUI at a job boundary.** `--drain-comfyui` waits until ComfyUI's queue is empty
  before stopping it; a job in progress is never cut off.
- **No juggling.** Two engines at the same priority drain each other: the last one asked for
  wins, which is what you want when you switch models. When two clients want different
  engines at once, that flips the card back and forth and every request fails. Berth calls it
  a juggle when an engine is drained within `juggle_window` (15 s) of getting the card while it
  is still busy (a request in flight, a ComfyUI job queued, a model loading). The engine ranking
  decides a juggle: the winner keeps or takes the card, and the loser waits for it instead of
  draining it back. An idle holder hands over at once, so chat, then an image, then chat again
  is three clean handoffs.

  ```toml
  [priority]
  order = ["llama-swap", "strata", "comfyui"]  # first is best; omit to rank by use
  juggle_window = "15s"                        # a negative value turns it off
  ```

  Without an `order`, the engine with the most hours on the card through Berth over the last
  7 days ranks first (kept in `~/.local/state/berth/usage.json`); a tie leaves the holder in
  place. `berth run --group NAME` files a lease under an engine family: llama-swap runs each
  model under its own name, so give each `--group llama-swap`. `berth status` shows the ranking
  and a count of juggles stopped; the harbor log shows each one with the reason.

  - **A minimum turn per engine.** `min_turn = "5m"` on the engine entry (or
    `berth run --min-turn 5m`) gives the engine a turn it keeps for that long after the
    grant: in that time a request of equal or lower priority cannot drain it, and neither can
    a higher one, because a request interrupted halfway is a lost one. Only a hold (a game, a
    call, `berth hold`) takes the card out before the turn ends.

    ```toml
    [[engine]]
    name  = "comfyui"
    min_turn = "5m"   # the card stays this engine's for 5m after the grant
    ```

    A blocked waiter is told how much of the turn is left ("next in line: comfyui has its
    minimum turn, 3m12s left"), and `berth status` shows the time a lease has left in the
    TURN column. When the turn ends the normal rules apply: a drain still starts at the slot
    or job boundary, and a minimum turn never cuts a request off. After a daemon restart, a
    re-attach starts a fresh turn.

### Games, apps and holds
- **Claims cover work Berth does not start.** berthd scans the process table every 2 s.
  A *hold* claim keeps the card while its process runs: engines are drained and wait, with a
  15 s linger so a launcher restarting the game does not bounce the card back. A *track*
  claim names an app's card memory so it counts against the card without being reported as a
  stowaway; nothing waits on it.
- **Built-in pack, on by default.** Holds: Steam games (anything under Steam's `reaper`),
  Lutris and gamescope on Linux; Steam libraries (`steamapps\common`), Epic, GOG, EA,
  Ubisoft, Xbox and Battle.net games on Windows, with launchers, Wallpaper Engine and
  Lossless Scaling left out. Tracks: Blender, DaVinci Resolve, OBS, Kdenlive, Premiere,
  After Effects. `berth claims` shows every rule and what it matches now.
- **Your own rules:**

  ```toml
  [[claim]]
  name = "training"
  cmdline = 'train\.py'      # regex; on Windows it sees the exe's full path
  action = "hold"            # or "track"
  linger = "30s"

  [claims]
  builtin = false            # turn the built-in pack off
  ```

  A rule can also match `exe` names (globs, `.exe` optional), a launcher `parent`, and
  exclude with `not_cmdline`.
- **`berth hold [--for 2h] [--reason game]`** takes the card for you. It is a pinned lease at
  the top priority, so nothing drains it, and it ends on Ctrl-C, after `--for`, or when the
  process dies.
- **`berth run --pinned`** gives an app a lease that is never drained (it keeps the card until
  it exits).
- A hold cannot free memory held by something outside Berth: that memory is still reported,
  as contested.

### Models
- **`berth pull hf:ORG/REPO[:QUANT|FILE|GLOB][@REV]`** resolves the revision to one commit,
  picks the GGUF for the quant (every part of a split model, and the vision projector when
  there is one), and downloads to `~/models/<org>/<repo>/`.
- **`berth pull civitai:VERSION_ID`** resolves a model version (or
  `civitai:model:MODEL_ID` for its latest, or the site's own URLs), downloads the files it
  lists to `~/models/civitai/<model>/`, and writes the same manifest. Public models need
  no key; gated files read `$CIVITAI_API_KEY`.
- **`berth civitai search QUERY`** and **`berth civitai versions MODEL_ID`** find the ids
  a pull takes: models by name and asset kind, then a model's versions newest first.
- **Integrity first.** Each file lands as `.part` and is moved into place only after its size
  and sha256 match the source's published hash. A mismatch deletes it and fails.
- **Resumes.** Run the same command again after an interruption and it continues with a
  range request. A file already present and correct is skipped.
- **The floor.** Before it downloads, a pull measures the destination root and refuses when
  its files would leave the root under `[library] free_floor` (a tenth by default, `0`
  turns it off). A root whose space this platform cannot measure says the floor was not
  checked, and goes on: a guard that cannot measure does not claim to guard.
- **Shelves.** `berth models` reads each model for which shelf it is: chat, image, audio,
  or unshelved when the file states nothing. A root declared in `[library]` (say
  `shelf = "image"`) declares what it holds, and that beats a file's header. The SHELF
  column shows it, `--shelf image` lists one shelf, and the totals under the listing say
  how much of the library each shelf holds.
- **The floor's view: `berth storage`.** Each root's filesystem (an ntfs3 root shared with
  Windows reads as ntfs3, on both sides of the share), the space it has left against its
  floor, what it can share (hard links, symlinks, reflinks), the other operating systems
  it is shared with, the shelves it holds, and when a berth last scanned it. A root under
  its floor fails the check.
- **The in-root catalog.** A scan leaves `<root>/.berth/catalog.json`: what it resolved,
  from which machine, and when. On a root two operating systems share, the second boot
  reads the first's resolutions instead of re-guessing them. The scan always recomputes;
  the file is a snapshot for the other side, never a cache this side trusts.
- **`berth verify`** re-hashes pulled models against the manifest the pull wrote
  (`.berth-manifest.json`: source, name, ref, every hash), offline, and exits 1 on any change.
- Gated and private models read `$HF_TOKEN` (Hugging Face) or `$CIVITAI_API_KEY` (CivitAI).
- **`berth models`** reads every GGUF header (never the weights) in the tools' usual places
  and any `--dir` or `[models] dirs`. Split models list as one; vision projectors and MTP or
  draft heads list with the model they belong to (`--all` shows them on their own).
  `--usage 30d` adds requests and tokens per file from the harbor's request log, matched
  through llama-swap's config when that is how the model is served.
- **The CACHED column.** How much of a model's files (its parts, and the vision projector and
  draft that go with it) sit in the kernel page cache, and the tier that share reads as: COOL at
  half or more, COLD below it. The check reads the kernel's page table (mincore), never a byte
  of a file, so it cannot pull a page in, and `--no-cache-check` skips it for a listing of a
  big library. The page cache is the kernel's: it keeps the pages until the machine needs the
  RAM, so the column says a restart is likely to read from memory, never that any RAM is held.
- **`berth prefetch MODEL_OR_PATH`** asks the kernel to read a model's files into the page
  cache (posix_fadvise WILLNEED). The ask is made and the command returns at once; the kernel
  does the reading in the background, at its own pace, and takes the pages back whenever the
  machine needs the RAM. This is the manual form: while an engine that is about to run is
  still waiting for the card, its model's files can be warmed so the start reads from memory.
  The harbor's LIBRARY panel carries the same ask as a copy button on every model row.
- **`berth models show NAME`** prints one model's header: architecture, context, tensor
  types, chat template, and what of the model sits in the page cache.
- **`berth models export ollama NAME [--dry-run]`** writes a Modelfile and runs `ollama
  create`; a split model needs `llama-gguf-split --merge` first.

### Gateway (preview)
- **One port for every engine.** `berth gateway` proxies OpenAI-style requests to the engine
  that serves the requested model, streaming.
- **Cold starts under a lease.** An engine with `start` and `serves` in `berth.toml` starts on
  the first request that names one of its models, through the `berth run` path: lease, drain
  of the holder, measured release, then the start. One start per engine, however many
  requests arrive while it loads.
- **Last switch wins.** Give gateway-started engines the priority of the engine they replace
  (`priority = "foreground"` next to a foreground llama-swap) and each side drains the other on
  demand. A lower priority waits behind the holder instead.
- **Clear failures.** Unknown model: 404. A start the broker refuses or an engine that exits
  before it answers: 503 with the reason, the engine's own failure line, a `Retry-After` and an
  error `code` a client can branch on: `outranked` (lost a juggle), `drained_while_starting`,
  `no_berth`, `not_ready`, `start_failed`, `engine_oom` (the engine could not get the GPU
  memory it asked for), `no_headroom` (a cold start would leave the machine short of RAM,
`--headroom-mib`) or `no_start`. An engine that crashes on start isn't started again on
  every retry: for 15 s, doubling to 5 min, the gateway answers at once with the same cause,
  and `Retry-After` says when the next start is allowed. A start that answers clears it. A
  model listed under `serves` by two engines fails `berth config check`.
- **API key.** Bearer token on every route; `berth key` prints it (created on first use, mode
  600). Loopback only; browser origins and non-loopback Host names are refused.

#### Switch engines by naming a model

Two engines that each want the whole card can't run together, and most harnesses can't start an engine
before they call it. `berth gateway` starts it for them. Give an engine a `start` command and the model names it
`serves` in `berth.toml`:

```toml
[[engine]]
name     = "strata"
type     = "llamacpp"
url      = "http://127.0.0.1:8080"
start    = ["/path/to/strata/.venv/bin/python", "serve/server.py", "--engine", "strata",
            "--config", "strata-262k.json", "--port", "8080"]
serves   = ["router-strata", "qwen3.8-flash-next-iq2_xs"]
priority = "foreground"   # equal to the engine it replaces, so the last model you ask for wins
prefetch = ["/models/strata"]   # read into the page cache while the holder drains
```

Run `berth gateway` (loopback `127.0.0.1:9799`; a user unit is in `contrib/berth-gateway.service`) and send
your requests there with the node's API key as the bearer token. The gateway creates the key on first start
(`~/.config/berth/api.key`, mode 600) and `berth key` prints it, so a client that runs commands for its key
never has the secret written into its config:

```json
{ "baseUrl": "http://127.0.0.1:9799/v1", "apiKey": "!berth key" }
```

For each request the gateway:

1. reads the model name from the body;
2. hands the request straight to an engine that is up and lists that model (llama-swap and Ollama load
   their own models under their own leases);
3. otherwise starts the engine that `serves` it: as its own `berth run` process, under the same lease
   path (Berth drains the holder: llama-swap through its unload URL once its slots are idle, then
   SIGTERM, then SIGKILL), and the gateway waits until the new engine answers `/v1/models`;
4. proxies the request and streams tokens as the engine sends them.

Requests that arrive during a start wait on that one start. A model no engine serves gets a 404, and a
start the broker refuses comes back as a 503 with the broker's reason. A start waits for the card no longer
than `--ready` (10 minutes); if the holder has not left by then, the request fails and the start is
dropped, so it cannot take the card hours later. Asking llama-swap for one of its
models later drains the gateway's engine the same way, so either side can take the card back.

**A slow engine can name a fallback.** An interactive client should not sit through a 90 s cold start
when an engine that needs no card can answer now. Give an engine a `fallback` (another engine in the
same file, such as a CPU llama-server) and a request that has waited `fallback_after` (10 s by
default) goes there instead, when the fallback answers its probe at that moment. The fallback is
never started, leased or drained by the gateway: one that is not answering means no fallback, and the
request keeps waiting for its own engine. The primary's start is never cancelled either way, so
requests that come later land on it. `fallback_model` names the model the fallback lists the request
under when the two disagree, a non-streaming answer carries `X-Berth-Fallback: <fallback>`, and a
streaming answer, whose headers went out with the keep-alive preamble, carries one
`: berth: fallback <fallback>` comment before its data. One hop, per engine, opt-in: `berth config check`
refuses chains and a fallback that is not an engine in the file.

```toml
[[engine]]
name     = "strata"
type     = "llamacpp"
url      = "http://127.0.0.1:8080"
start    = ["/path/to/strata/.venv/bin/python", "serve/server.py", "--engine", "strata", "--port", "8080"]
serves   = ["qwen3.8-flash-next-iq2_xs"]
priority = "interactive"
fallback       = "strata-cpu"
fallback_after = "10s"
fallback_model = "qwen3.8-cpu"   # what the CPU server lists the model as

[[engine]]
name  = "strata-cpu"
type  = "llamacpp"
url   = "http://127.0.0.1:8010"
start = ["llama-server", "-m", "/models/qwen3.8-cpu.gguf", "--port", "8010"]   # no GPU
serves = ["qwen3.8-cpu"]
```

**A start reads the engine's files into the page cache while the holder drains.** Give an engine a
`prefetch` list of absolute files and directories, and the gateway asks the kernel to read them the
moment it decides to start that engine, before its `berth run` waits for the lease. The kernel reads
on its own schedule and takes the pages back when the machine needs the RAM, so a warm cache commits
no RAM of its own, and a start that finds its files still cached reads them from memory. A model
larger than the RAM the machine can spare gains little: its pages are pushed out as they are read. A directory means
its own files and not its subdirectories, and a file that cannot be asked for is one WARN line in the
gateway's journal while the start goes on. `berth prefetch MODEL` makes the same ask by hand.

Measured on the reference RTX 4090 with the [swap bench](contrib/bench/README.md) switching between
NInfer (through llama-swap) and Strata (262K context), both on an NVMe SSD, 2026-10-20 (two runs,
five switches each way):

| Switch | Drain of the holder | Request answered after |
|---|---|---|
| NInfer on an empty card | none | 12.2 s |
| Strata to NInfer | Strata released in 2.2 to 4.6 s | 15.4 to 17.5 s |
| NInfer to Strata, NInfer idle | NInfer released in 2.7 s | 74.8 s |
| NInfer to Strata, inside the juggle window | NInfer released in 1.5 to 3.1 s, after about 5 min | 71 to 85 s after the grant (Strata's own load) |

The bench asks for the other model the moment an answer ends, inside the 15 s juggle window, so
most switches to Strata also waited about 5 minutes behind the guard until llama-swap unloaded
NInfer on its own (374 to 388 s end to end). The one switch that came after NInfer sat idle
drained it at once.

At no point did the two engines hold the card at the same time.

**A gateway restart keeps its engines.** Each engine runs as its own `berth run` process, in a systemd
user scope on Linux (`systemd-run --user --scope`, when the user manager is reachable) so it is outside
the gateway service's cgroup, else in a new session. The gateway writes each started run, with its PID,
start time and lease, to a small file under the state directory (`~/.local/state/berth/gateway-engines.json`),
and the run's own output goes to `~/.local/state/berth/engines/<name>.log` (mode 600), never to a pipe the
gateway holds, so a gateway that stops cannot break an engine's writes. While the gateway runs it copies
the log to its own journal as well.
When the gateway comes back it reads that file and adopts every engine whose process is still the same
one, answering or still loading: the next request is proxied, not started again. Records for processes
that left are dropped. Restarting during a cold start (up to 85 s for Strata) costs nothing but the
restart itself.

**Stopping the gateway keeps its engines too.** Each engine runs outside the gateway's cgroup (above),
so stopping the gateway service does not stop the engines: they stay on the card under their own leases
and keep answering. `berth stop ENGINE` is how one is stopped on purpose: the daemon asks the engine's
leases to give the card back through the normal stop ladder, as when another engine asks for it, and a
lease frees only after a card read shows the engine's memory gone. `berth stop ENGINE --wait` follows
the daemon's event log until the leases are freed (exit 0) or stuck (exit 2, pointing at `berth reclaim`
for the stuck ones).

**Long waits and idle clients.** Cold starts take 12-85 s (table above), and minutes when the juggle
guard holds a switch back: longer than many OpenAI clients wait before giving up. Streaming requests get the answer early: the gateway sends its SSE
headers (`Content-Type: text/event-stream`, `Cache-Control: no-cache`) right away and writes an SSE
comment every 10 s while the start runs, `: berth: loading strata, 34 s`, so proxies and clients keep
the connection and a reader sees the wait moving. Once the engine answers, its body streams on; a
start that fails sends one OpenAI-shaped error event with the same reason code the 503 path carries,
then `data: [DONE]`. Non-streaming requests are unchanged; if a client drops a long non-streamed
wait, raise its read timeout or make the request streamed. `GET /v1/berth/wait?model=NAME` blocks
until the engine answers (a failed start is the same 503), so a harness can pre-warm before its
first real request. The gateway serves `/v1/chat/completions`,
`/v1/completions`, `/v1/embeddings` and a merged `/v1/models`, plus the ComfyUI surface: `POST /prompt`
queues a workflow (starting ComfyUI under a lease when it is down, holding the request until its `/queue`
answers), while `/ws`, `/queue`, `/history/`, `/view` and `/upload/` proxy to a running ComfyUI. Every route
needs the key. The gateway listens
on loopback only, refuses a Host that is not a loopback name (DNS rebinding) and refuses any request that
carries a browser `Origin`, so a web page cannot start an engine. ComfyUI already hands off at a job boundary under a lease (`--drain-comfyui`); a drain first unloads its models via `POST /free`, and `--keep-comfyui-up` leaves the server on its port for OpenWebUI instead of stopping it.

#### What a switch costs

The gateway runs a start and sees both ends of it: the grant its run printed, which is
when the card showed free, and the engine's first answer. It times that pair for every
start it runs and keeps the last 20 times per engine and tier in a book
(`~/.local/state/berth/swapcost.json`), so it can price the next switch without guessing.
The wait for a free card belongs to the queue, not to the engine, so it is not part of the
figure. A failed start records nothing, and so does an engine the gateway does not start itself
(llama-swap's models load under llama-swap, so the gateway has nothing to time for them).

`GET /v1/berth/cost?engine=NAME` answers one estimate per tier, hot first. On the reference machine,
after five gateway starts of Strata from disk:

```bash
curl -s "http://127.0.0.1:9799/v1/berth/cost?engine=strata" -H "Authorization: Bearer $(berth key)"
```

```json
{"engine":"strata","estimates":[
  {"tier":"hot","took_ms":0,"samples":0,"known":true},
  {"tier":"warm","took_ms":0,"samples":0,"known":false},
  {"tier":"cool","took_ms":0,"samples":0,"known":false},
  {"tier":"cold","took_ms":72400,"samples":5,"known":true}]}
```

Hot, the engine already holding the card, is the free switch. Warm is a parked engine,
its weights in host RAM. Cool and cold split at half of the engine's files still held by
the kernel page cache. An estimate with `known:false` has no samples behind it, and its
`took_ms: 0` is not a prediction: a figure the gateway gives is one it measured.

`POST /v1/berth/plan` prices a chain an orchestrator declares ahead of time, one step per
switch, each naming the engine and the tier to find it in:

```bash
curl -s http://127.0.0.1:9799/v1/berth/plan -H "Authorization: Bearer $(berth key)" \
  -d '{"chain":[{"engine":"strata","tier":"cold"},{"engine":"comfyui","tier":"cold"},{"engine":"strata","tier":"cold"}]}'
```

```json
{"steps":[
  {"engine":"strata","tier":"cold","took_ms":72400,"samples":5,"known":true},
  {"engine":"comfyui","tier":"cold","took_ms":0,"samples":0,"known":false},
  {"engine":"strata","tier":"cold","took_ms":72400,"samples":5,"known":true}],
 "total_ms":144800,
 "unmeasured":1,
 "prefetch":["comfyui","strata",""],
 "keep_parked":["strata"]}
```

`total_ms` sums the known steps only: with `unmeasured` above 0 it is a lower bound, read
as "at least 145 s, 1 step unmeasured", never a promise. `prefetch` is per step, the engine
to warm while that step holds the card, and `keep_parked` lists the engines the chain comes
back to, which are worth parking in RAM instead of stopping and reloading. An unknown tier
or an engine `berth.toml` does not name is a 400 naming the step; a missing or wrong key
and any browser `Origin` are refused, as on every other route.

#### Open a stopped engine's web UI

A bookmark to ComfyUI or Strata's chat page is a dead link while that engine is stopped. Give the engine a
`gui` URL (where a browser reaches its web UI) next to its `start` command, and run the gateway with
`--gui-listen 127.0.0.1:9796`:

```toml
[[engine]]
name  = "comfyui"
type  = "comfyui"
url   = "http://127.0.0.1:8188"
start = ["comfy", "--workspace", "/path/to/comfy", "launch", "--", "--listen", "127.0.0.1", "--port", "8188"]
gui   = "https://box.tailnet.ts.net:8188/"
```

Point the bookmark (or a home-page tile) at `http://127.0.0.1:9796/gui/comfyui` instead:

- **ComfyUI answers:** the browser goes straight to its UI.
- **ComfyUI is stopped:** the page says so and lists what holds the card (`llama-swap (NInfer-27B)  active`), with a
  **Start** button. Opening the page takes nothing; only Start asks for the card.
- **After Start:** the page says what the start waits for, in the broker's terms: "Waiting for strata release:
  finishing its current request", "Waiting for llama-swap (NInfer-27B) release: freeing its memory", then
  "loading comfyui". It opens the UI once ComfyUI answers. A failed start shows its reason code and a retry button.
- `/gui/` lists every engine with a wake page.

A start never runs two engines on the card at once. Berth drains the holder only at its boundary: llama-swap once its
slots are idle (`drain_post`), ComfyUI once its queue is empty (`drain_comfyui`), and a server with no unload URL
once its `/slots` show idle (`drain_slots`, below). A pinned holder (a game, a render, `berth hold`) is never stopped;
the page says so and the start waits until it ends.

The wake page needs no API key: it can only start an engine you gave both `start` and `gui`. It listens on
loopback, accepts loopback Host names plus the gateway's `--allow-host` names (for `tailscale serve` in
front), and takes a start only as a POST from its own page, so another site cannot make a visitor's
browser drain the card.

#### Drain a server at its request boundary

An engine with `/slots` but no unload URL (Strata, a bare llama-server) would otherwise get SIGTERM mid-request when
another engine asks for the card. `drain_slots` (or `berth run --drain-slots URL`) names its base URL: a drain waits
until no slot reports `is_processing`, then stops it.

```toml
[[engine]]
name        = "strata"
type        = "llamacpp"
url         = "http://127.0.0.1:8080"
drain_slots = "http://127.0.0.1:8080"
```

The wait is unbounded by default, so a long generation is never cut off.
`drain_max` (or `berth run --drain-max DURATION`, which wins) sets a ceiling
on it: while the boundary is busy the wrapper prints a line each minute
naming the slots and how long they have been busy, and once the ceiling
passes it runs the normal stop ladder anyway and says so. With `--fail-closed`,
it refuses the drain and keeps the child and lease instead. The same ceiling
applies to a ComfyUI queue that will not empty.

Fail-closed also rejects missing or malformed slot states and unreadable queue states.
Without an explicit `drain_max`, slot checks and continuously unreadable ComfyUI queues
refuse after four times the run's grace interval. A readable busy ComfyUI queue still
waits without a deadline. A later drain can retry after the boundary recovers.
The default fail-open policy and native parking path remain unchanged.

#### End a ComfyUI session

`exit_comfyui_idle = true` (or `--exit-comfyui-idle`) releases a run after
its first completed job. Existing history does not count as new work.
`exit_comfyui_idle_after` (or `--exit-comfyui-idle-after`) also releases a
session that receives no work; zero leaves that wait without a deadline.
These options require `drain_comfyui` and cannot combine with `keep_comfyui_up`.

The session detector and daemon drains share one drain ticket. A refusal retains
the original session deadline and work evidence, then retries after recovery.
A readable busy queue is never treated as idle because history is unavailable.
Completed-session release ends the process; daemon-requested parking and resume
keep their native protocol and lease routing. Without these options, behavior
is unchanged.

#### Park in RAM (warm switch)

A drain today ends with the engine's weights on disk, and the next request
reloads them: tens of seconds every switch. Parking keeps the warm copy in
host RAM instead:

| Tier | Where the weights are | Switch back | RAM cost |
|---|---|---|---|
| hot | VRAM | now | none |
| warm (parked) | host RAM, held by the engine | seconds | the footprint, committed while it lasts |
| cool | the kernel page cache | load minus disk | nothing committed |
| cold | disk | full cold start | none |

Config, one line per engine plus the node's budget:

```toml
[[engine]]
name       = "vllm-chat"
type       = "vllm"
url        = "http://127.0.0.1:8000"
park       = "hook"
park_url   = "http://127.0.0.1:8000/sleep?level=1"   # optional: each beats the built-in
resume_url = "http://127.0.0.1:8000/wake_up"

[park]
floor_gib = 4.0        # RAM a park must leave the machine (default 4)
max_gib   = 48         # cap on all parked copies together (default: no cap)
ttl       = "30m"      # a parked copy is stopped after this long
residual_max_gib = 2   # card memory a parked engine may keep
```

- **How it runs.** A drain first waits for the engine's boundary (idle
  slots, or an empty ComfyUI queue), then asks berthd to admit the park:
  admission measures the machine's available RAM, and a park that would
  leave less than the floor is refused quietly, so the drain runs as
  today. On admission the wrapper calls the engine's own offload hook and
  records the park; the lease stays parked, the card's residual counts, and
  a waiter builds beside it. The next request sends the resume, and the
  engine serves again in seconds.
- **Adapters.** ComfyUI needs nothing: its unload keeps the models in RAM
  until memory pressure. vLLM needs `--enable-sleep-mode` and
  `VLLM_SERVER_DEV_MODE=1` in the start command. SGLang needs
  `--enable-memory-saver --enable-weights-cpu-backup`. Any other engine can
  speak the park protocol (`contrib/PARK-PROTOCOL.md`): Berth probes
  `GET /berth/state` once and uses it when the endpoint answers.
- **Eviction.** A parked copy is stopped, least recently parked first,
  when it passes its ttl, when the machine's available RAM falls under the
  floor, or one step at a time under memory pressure. Stopping a parked
  engine is a drain, and its run is Berth's own, so the stop is graceful.
- **Who sits out.** llama.cpp and llama-swap have no offload call upstream
  yet, so they run today's drain; a park hook there waits for an upstream
  endpoint or a patched route.

### Visibility
- **Stowaways:** every process holding VRAM without a lease, with memory, identity and
  first-seen time; optional desktop notification and webhook. Report-only by design.
- **Contested grants:** a grant made while stowaways hold memory is flagged, not hidden.
- **Event log:** every request, wait, drain, release, free, stuck lease, stowaway and
  probe change, with gap-free sequence numbers; `berth events -f` follows it live.
- **The collisions board:** collisions avoided, VRAM handed off, drains, stuck releases
  caught, stowaways caught, contested grants, refusals. Each count links back to a
  recorded event. berthd keeps the board in `~/.local/state/berth/counters.json`
  (`--counters`), so a restart or a reboot carries the counts on. The harbor stores a
  reading a minute and its ticket counter shows the last 5 min, 15 min, hour, 24 h or
  7 days next to the all-time board (`GET /v1/counters?range=5m|15m|1h|24h|7d`).
- **The engines' estimated VRAM:** a run measures how much VRAM its leases held at their
  peak, and the broker keeps the last figure per engine in `~/.local/state/berth/footprint.json`
  (`--footprint`), across restarts. `berth status` shows it as EST. VRAM beside the
  engine's own figures, and the JSON names it `estimated_vram_bytes`. It is an estimate
  of what a run needs, never a reservation: a run that holds far more than the learned
  figure (1.5x, once the engine has three runs) logs an `engine.footprint` event, so a
  grown engine is seen instead of mis-sized.
- **The queue in every wait reason:** every reason a waiter is sent starts with its place
  in the queue, "next in line" or "2nd in line (behind interactive comfyui)", so a client
  behind the head of the queue hears where it stands instead of silence. The waiting table
  of `berth status` shows the same place beside a start estimate, in seconds after the card
  frees: the measured starts in the waiter's chain, one per seat ahead plus its own. An
  unmeasured start says unmeasured and adds no number, and the holder's drain is never in
  the figure, because a request runs as long as it runs.
- **Alerts:** one threshold table in `berth.toml` (`[alerts]`): warn and crit per key
  (`queue`, `ttft`, `tpot`, `p95`, `cache`, `reingest`, `vram`, `temp`, `power`, `kv`).
  The node evaluates it on its 5 s sample cycle, fires a desktop notification and webhook
  on entering a level, and logs `alert.raised` / `alert.cleared` events (`berth events
  -f`, the harbor log); a level stays until the value stays clear for `clear_polls`
  samples (default 3). GPU probe failure and aground leases are built-in danger alerts,
  always on. Default posture: no `[alerts]` table means no KPI alerts, and the
  notification channels are separately opt-in (`--notify`, `--webhook`). The harbor
  colors the same state the node notifies on: KPI tiles and GPU cells, a strip of the
  currently-breaching keys, and an overall LIVE / DEGRADED / OFFLINE state beside the
  connection pill.

### The harbor's views
Everything the node collects shows in one of three tabs, and every panel answers a
question an operator actually asks.

**The CARD tab.**
- **The ticket counter** answers "has anything collided?" with the collisions-avoided
  odometer, the seven collision tiles and the request KPIs, over the all-time board or
  a stored window.
- **The tide** answers "who holds the card, and when?" with card memory by holder and
  the lease events overlaid, over a 1H / 6H / 24H / 7D / 30D picker.
- **The model timeline** answers "when was each model loaded on each engine?"
- **The departures board** answers "what is on the card right now?" with one row per
  lease: the ticket number, the engine, the class, the hold (measured once docked,
  asked-for while boarding, the parked engine's RAM copy while WARM), the state word,
  and the age. A WARM flag marks a parked engine; PARKING is the transitional state
  while the weights move to RAM. A boarding row carries a Q flag with its queue
  position and the node's completion estimate when the node sends them. Clicking a
  row opens the lease's ticket: the tier the weights live in (HOT on the card, WARM
  in RAM), the queue rows, the container a run is in, and the exact terminal order
  for the lease beside it as a copy button (`berth stop ENGINE`, `berth resume
  ENGINE`, `berth reclaim LEASE`).
- **The berths, the machine, stowaways and no ticket** answer "what else is on the
  card and the host, and who holds memory without a ticket?"

**The ENGINES tab.**
- **The engine cards** answer "is this engine up and serving?" with the state the
  engine reports, its loaded models, decode and prefill rates, and its learned
  footprint as EST. VRAM: what its past runs measured, never "reserved".
- **The library** answers "is this model warm?" with every model in the configured
  roots, each row showing shelf, quant, size, the CACHED share with its tier
  (COOL or COLD), the engine serving it when one reports it resident, that engine's
  EST. VRAM, and copy buttons for `berth prefetch NAME` and `berth models show NAME`.
  The listing is read-only here; the terminal does the acting.
- **Card ranking, the KV pool, throughput, the token ledger, the leaderboard, the
  recent requests and context residency** answer the questions behind the request
  log, and the storage panel (advanced) says what the harbor's own store keeps.

**The ABOUT tab** is this page's own documentation: the modes, the shortcut keys,
and what every state word and figure means.

### Telemetry (built in, no other tool needed)
- **GPU:** memory, utilisation, temperature, power against its limit, fan, SM and memory
  clocks, PCIe link, for **every card** in the machine with its real limits.
- **Host:** CPU, RAM, swap, load.
- **Engines:** every engine in the table above, found on its default port or configured on
  any other. Loaded models, decode and prefill tok/s, token totals,
  queue, draft acceptance, the context window per model (llama.cpp/NInfer `/props`, Strata
  `/health`, llama-swap per model proxy), and the live KV slot pool (`/slots`: per-slot
  context tokens against the window). An engine without those endpoints degrades to
  "no window / no slot data", never an error. An engine that was up and stops
  answering is shown as **unresponsive** (latched), distinct from one that never ran.
- **Requests:** a 24-hour request log from llama-swap; requests per minute, p50 and p95
  latency, prefill time, time per output token, cache and re-ingest share, a token ledger
  (1 h, 24 h, since start), a per-model leaderboard, and the most recent requests.
- **History:** the node keeps the last hour at 5-second resolution in memory. The harbor
  stores it on disk (SQLite, no C toolchain needed): 1-minute rows for 30 days, per engine
  the KV pressure (peak slot fill against the window) and the resident models' windows,
  hourly rows for 400 days, and the request log for 30 days, de-duplicated by request id.
  Databases written before the KV column is added are migrated in place on the first
  open; nothing is ever rewritten.
- **Model timeline:** when each model was loaded and unloaded on each engine, kept across
  restarts. A gap in the readings over 3 minutes is never drawn as loaded.
- The UI labels engine-reported figures as the engine's own, apart from what the driver
  measures.

### Interfaces
- **CLI:** one command tree with one output contract. `berth status`, `berth events -f`,
  `berth run`, `berth hold`, `berth stop`, `berth claims`, `berth pull`, `berth verify`, `berth models`, `berth gateway`,
  `berth wire llama-swap`, `berth engine check`, `berth config check`, `berth config show`,
  `berth export`, `berth key`, `berth skill install|show`, `berth update`, `berth version`,
  and for the node itself `berth daemon`, `berth harbor`, `berth service install|uninstall|status`,
  `berth reclaim`, `berth resume`, `berth prefetch`, `berth storage`, `berth civitai search|versions`
  and the ComfyUI suite under `berth engine install|show|update|rollback|remove`.
  Berth runs fully headless.

  ```sh
  berth wire llama-swap --config cfg.yaml          # plan: which models lack a wrapper
  berth wire llama-swap --config cfg.yaml --write  # apply; a backup stays beside the file
  berth wire llama-swap --config cfg.yaml --check  # exit 1 when a model is unwired
  berth config check --strict          # a warning fails the check, exit 1
  berth config show --format toml      # the evaluated config, ready to save
  berth engine check -v                # what each engine reports, right now
  berth -q status; echo $?             # exit codes only, for CI
  berth status --format json | jq .leases
  ```

  Results go to stdout, diagnostics go to stderr. `--format` is `human` (the default), `json` or
  `yaml`, and `berth config show` also takes `toml`, the config's own file format. `-v` adds
  detail to the human view, and `-q` suppresses stdout and leaves the exit code as the answer.
  Exit codes are 0 (done), 1 (validation or logic: an invalid config, a warning under `--strict`,
  an engine that did not answer, a lease the broker refused) and 2 (berth could not do its job:
  unknown flag, missing file, berthd unreachable). `berth run` passes the engine's own status
  through and exits 75 when no berth was granted. Every read-only command changes nothing.
- **Harbor web UI** (`berth harbor`): a departures board of every lease (the HOT / WARM
  tiers included: WARM marks a parked engine, with its parked RAM and age), a boat ticket
  per lease, the collisions counter, berths with measured memory, stowaways, the event
  log, and engine metrics. The tide (card memory by holder) and the model timeline share a
  1H / 6H / 24H / 7D / 30D picker; the Engines tab adds the library (every model on the
  machine with its shelf, quant, size, cached tier, the engine serving it and that
  engine's EST. VRAM, with the terminal commands one row of copy buttons away), a queue
  KPI (the berth's own queue; each engine's own queue stays on its card), the KV pool (the live slots of each
  engine against their model windows, stored ranges the per-minute peak slot fill), context
  residency (the last requests' token figures against the model's window, plus the live
  KV sessions), a throughput chart, a 24H / 7D / 30D leaderboard and, in advanced mode, a
  storage panel. The top bar shows the overall state (LIVE / DEGRADED / OFFLINE) and a
  strip of the alerts currently breaching, colored from the node's own threshold state.
  Basic and advanced modes. While its update check is on and a newer
  release exists, the header shows it: the release, a link to it, and a
  button that copies `berth update`, the command that does the replacing.
  Read-only, strict content security policy. It listens on loopback by default
  (`127.0.0.1:9798`); passing a non-loopback `--listen` address (e.g. `0.0.0.0:9798`;
  a specific interface IP works too) is the explicit opt-in to expose it on your
  network: the startup logs a warning and the page shows a banner on every screen while
  exposed. There is no authentication in this release: expose it only on a network you
  trust; phones on that network get the full read-only dashboard. To reach it from elsewhere
  without exposing it, keep the loopback listener and put a same-machine reverse proxy in
  front (`tailscale serve`, caddy), passing the proxy's hostname with `--allow-host`. A systemd user unit is
  in `contrib/berth-harbor.service`.
- **Prometheus** `/metrics` for Grafana and friends, on the `[metrics] listen` address. It
  serves loopback only and refuses a `Host` that is not a loopback name, so a web page cannot read
  it through DNS rebinding. The endpoint names your engines, leases and queue, so Berth never
  serves it off the machine. To scrape it from a Prometheus on another host, keep the endpoint
  as it is and either:
  - run a Prometheus agent or Grafana Alloy on the Berth machine, scrape `127.0.0.1`, and
    `remote_write` to your Prometheus; or
  - put Caddy or nginx on the same machine in front of it, sending a loopback `Host`
    (`header_up Host 127.0.0.1`, `proxy_set_header Host 127.0.0.1;`), with the proxy's own
    allowlist or auth on the network side. `tailscale serve` passes the tailnet name as `Host`,
    which `/metrics` refuses.
- **Footprint:** the node uses about 0.2% of one CPU core and ~21 MB of memory on the
  reference box; the harbor about 0.4% and ~23 MB, writing to disk once a minute.

---

## Tested hardware

The scenarios in [contrib/chaos](contrib/chaos/README.md) and the numbers below are how
Berth's failure handling gets checked outside this tree. Run them on your card and add a row
with the result, or report what happened with the
[drain report issue form](https://github.com/T-Crypt/berth/issues/new?template=drain-report.yml)
and the table credits you here.

| GPU | VRAM | Driver | OS | Engines | Result | Tester | Date |
|---|---|---|---|---|---|---|---|
| RTX 4090 | 24 GB | 615.78.08 | Arch Linux | llama-swap + NInfer + Strata + ComfyUI | daily use | T-Crypt | 2026-10-20 |
| RTX 4090 | 24 GB | 615.78.08 | Arch Linux | contrib/chaos scenarios 1, 2, 4 | pass | T-Crypt | 2026-10-20 |
| RTX 4090 | 24 GB | 615.78.08 | Arch Linux | contrib/chaos scenario 3 (parent exits at once) | reported as a stowaway, never signalled: the known window | T-Crypt | 2026-10-20 |
| RTX 5070 Ti + RTX 3060 | 16 GB + 12 GB | - | Windows 11 | the node, claims and the harbor, no engines | found [#81](https://github.com/T-Crypt/berth/issues/81) and [#82](https://github.com/T-Crypt/berth/issues/82) | mesquitafmr | 2026-10-21 |

---

## Swap numbers

`contrib/bench/swap` through the gateway on the RTX 4090 workstation (driver 615.78.08, PCIe 4.0
x16, 62.5 GiB RAM, models on a PCIe NVMe SSD), 2026-10-20: an opening request to A, then three
cycles of B and A, a fixed prompt, greedy.

| Engine | Cold first token | Warm first token (median) | Engine's own load |
|---|---|---|---|
| NInfer-Qwen3.8-27B (llama-swap) | 12.2 s | 16.6 s | part of the first token |
| Strata, Qwen3.8-Flash-Next UD-IQ4_XS | 387.5 s | 379.2 s | 85 s cold, 71 to 75 s after |

Switching on every request is the ping-pong Berth's juggle guard exists to hold back: each switch
to Strata waited about 5 minutes behind the guard until llama-swap unloaded NInfer, and the
engine's own load (from the lease grant to its ready line) is the last column. The bench's
"output identical" check is left out: both models think before they answer, the 32 tokens it asks
for end inside the thinking, and it compared empty answers.

**Where Strata's load goes.** The model file is 87.2 GiB and Strata page-locks 40 GiB of RAM
while it runs, so on this 62.5 GiB machine its file never stays in the page cache (7% cached after
the run) and every start reads it from disk. Moving the file from a SATA SSD to the NVMe SSD cut
the load from 142 to 160 s to 54 to 85 s. Of today's 71 to 85 s, reading the weights and filling
the GPU's expert cache took about 18 s; the rest is Strata's own start-up after it reports
"almost ready", which no disk or cache speeds up.

**Machines with RAM to spare.** Neither engine here can park, so these are cold reloads, not the
warm tier. On a machine with enough RAM to keep a model's file in the page cache (`berth models`
shows it as COOL, `berth prefetch` warms it), the disk part of a reload comes from RAM instead;
an engine that parks ([park in RAM](#park-in-ram-warm-switch)) skips the reload. Neither number
is measured yet: the reference machine has no RAM to spare beside Strata. If you run 128 GB or
more, run the bench with park on and add a row.

---

## Support matrix

| Platform | Status |
|---|---|
| Linux + NVIDIA | Supported |
| Windows 10/11 + NVIDIA | Experimental: leases, process attribution, job-object drains and game claims work, and the test suite runs on Windows in CI. Per-process VRAM comes from the GPU Process Memory counter (Task Manager's source), which also lists graphics apps and games nvidia-smi omits; host RAM and the page file come from GlobalMemoryStatusEx, and root space from GetDiskFreeSpaceEx. `berth service install` sets the node up as logon tasks; still no packaged installer and no locked-down control pipe. `contrib/windows/check.ps1` compares Berth's per-process VRAM with Task Manager's and writes a report for the drain report form. |
| AMD on Linux | Not supported yet; planned for v0.4 (a vendor-neutral probe, then amdgpu) |
| Intel, Apple | Not supported yet |
| Multiple GPUs | Inventoried with real limits; leases schedule one card per daemon today |

---

## Roadmap

| Phase | What it delivers | Status |
|---|---|---|
| P0 | Safety core: identity, measured release, stuck and contested detection, unprotected runs | **Done** |
| P1 | Node API: event log, collisions board, config, engine readers, `/metrics`, light probe | **Done** |
| P2 | Harbor web UI; native telemetry, request KPIs, ledger, leaderboard, stored history, measured against Speculum parity | **Done** |
| P3 | `berth hold` and claims for games and apps (**done**); drain and hold from the harbor, behind authentication (planned) | Partly done |
| P3.5 | Node gateway: one OpenAI-compatible port; engines start on demand under a lease and drain each other | **Preview** |
| P4 | Restart reconciliation: engines re-attach after a daemon restart, nothing left unprotected | **Done** |
| P5 | Fleet: `berth install --harbor <addr> --join <token>` on every machine; nodes push metrics into the harbor's store (already built for one box in P2) | Planned |
| P5.5 | Harbor router: one URL serves models from any node in the fleet | Planned |
| P6 | ComfyUI job-boundary handoff on Linux (**done**); ComfyUI started on demand through the gateway (**done**); Windows node (**experimental**) | Partly done |
| P7 | Offers: a waiting request can offer for an idle card on another machine; the holder decides | Planned |
| P8 | Multi-GPU scheduling: a lease table per card, "any card that fits" | Planned |
| P9 | Organisations: single sign-on, role-based access, audit log, Postgres, regional federation; a mobile app after | Planned |
| P10 | Opt-in auto-yield: a card's owner can pre-approve offers for their own card | Later |

### Shipped: v0.3, fast swap and proof (2026-10-20)

v0.3.0 went after the cost you feel most, a reload from disk on every switch, and after proof
that Berth holds up when an engine misbehaves. It shipped park in RAM with adapters for ComfyUI,
vLLM and SGLang and an open [park protocol](contrib/PARK-PROTOCOL.md); `berth stop`, `berth resume`
and `berth reclaim`; engines in Docker and Podman; a place and an estimate in every wait reason;
`berth update` and `berth service install`; the page-cache CACHED column and `berth prefetch`;
per-process VRAM on Windows and the test suite on Windows in CI; and the proof kit:
[contrib/chaos](contrib/chaos/README.md), [contrib/bench/swap](contrib/bench/swap), the drain report
form and the [tested hardware](#tested-hardware) table. v0.3.1 fixed `berth update` answering from
a stale cache. The [CHANGELOG](CHANGELOG.md) has every entry.

### Shipped since: v0.3.3 (2026-10-21)

v0.3.2 carried the first Windows testers' reports: the Epic launcher no longer holds the card from
the tray ([#81](https://github.com/T-Crypt/berth/issues/81)), per-process VRAM counts only the
governed card ([#82](https://github.com/T-Crypt/berth/issues/82)), and `berth service install`
starts its Windows tasks ([#89](https://github.com/T-Crypt/berth/issues/89)), with the minimum turn,
keys on drain requests and fail-closed drain boundaries beside them.

v0.3.3 carries the next round. The squatter total stops at the memory the driver reports in use, so
a Windows compositor's committed reservation no longer blocks admission
([#103](https://github.com/T-Crypt/berth/issues/103)). A lease whose `berth run` process dies after
attach frees itself ([#104](https://github.com/T-Crypt/berth/issues/104)), and the Windows check kit
works again ([#105](https://github.com/T-Crypt/berth/issues/105)). The gateway warms the next
engine's files during a drain (`prefetch`) and serves measured switch costs at `/v1/berth/cost` and
`/v1/berth/plan`.

### Next: v0.3.x

In the next v0.3.x releases. Each item has its building blocks merged or a spec written:

- Cgroup attribution, which closes the one known stowaway window (chaos scenario 3: a child
  whose parent exits within one 2 s tick).
- KV snapshots wired into the drain and start paths, so a sequence of agents skips the prefill
  after each switch.
- Measured switch costs on the live daemon.
- Gateway fallback routing, and models started on demand for a standalone llama-server.
- Parked resume numbers in [Swap numbers](#swap-numbers), which take the vLLM and SGLang park
  adapters out of experimental, and a measurement of NVIDIA's `cuda-checkpoint` for unmodified
  CUDA engines on Linux.

Not started, and in no release yet: `berth fit` and scenes, so engines that fit together never swap.

### After v0.3.x

| Release | What it brings |
|---|---|
| v0.4 | AMD on Linux, through a vendor-neutral probe; the Windows node out of experimental (a packaged installer, a locked-down control pipe) |
| v0.5 | Multi-GPU scheduling, including one lease across two cards for engines that split a model |
| v0.6 | The fleet: Berth on every machine, one harbor, one URL for every model |
| later | Offers: a waiting request can ask for an idle card on another machine, and its holder decides |

On hold: engine installers beyond the ComfyUI suite, the migration manager, and Berth swapping
models itself. llama-swap already swaps models well. Berth works beside it, and a proposal to
llama-swap for generic load hooks is planned, so Berth needs no wrapper line.

## What Berth never does

- Kill a process it did not start, or any process by default.
- Grant a card because a process exited, before the driver shows the memory free.
- Claim memory is reserved when it is only estimated to be free.
- Take a card from another user's machine without their consent (on one machine, only the
  priorities you set decide).
- Require a cloud account, telemetry upload, or any other software to work.

## FAQ

**What is an engine?** A program that runs a model on your GPU: llama.cpp, llama-swap, Ollama,
vLLM, ComfyUI, a voice server. Berth doesn't run models; it decides which engine has the card.

**Why not just llama-swap?** Use both. llama-swap swaps the models in its own config. Berth
covers the whole card, including ComfyUI, games and the stray process holding memory that
neither tool started. See [Berth and llama-swap](#berth-and-llama-swap).

**Can a 700B model take turns on a 24 GB card?** No. Berth gives each engine the whole card in
turn, or a sized share with `--needs-mib`; it doesn't make a model fit that doesn't.

**How reliable is it?** Alpha. It runs every day on the author's RTX 4090 workstation, and v0.3
added `contrib/chaos`, the swap bench and the [tested hardware](#tested-hardware) table so you can
check it on yours.

**AMD? Windows?** AMD on Linux and a full Windows node are planned for v0.4. Windows with NVIDIA
works today as experimental, and `contrib/windows/check.ps1` checks it on your machine; see the
[support matrix](#support-matrix).

**Can Berth handle ComfyUI on the host and vLLM in Docker?** Yes. Start each one under its own
lease: `berth run --as comfyui -- python ...` for ComfyUI, and
`berth run --as vllm --container vllm -- docker run --rm --name vllm --gpus all ... vllm/vllm-openai`
for vLLM. The container's GPU processes are parented by containerd-shim, so the process tree
cannot reach them; the lease names the container instead, Berth matches the processes by
container ID, and the drain runs `docker stop -t 30 vllm` (podman's when podman resolved it)
instead of signalling the wrapper. The handoff waits for the driver to show the container's
memory gone, as with a host process. A container Berth did not start is still a stowaway:
reported with its container ID, report only, never signalled. Details in
[contrib/INTEGRATION.md](contrib/INTEGRATION.md).

## Contributing

Issues and pull requests are welcome. [AGENTS.md](AGENTS.md) has the one command that proves a
change (build, format, vet, tests, lint, as CI runs them) and the invariants a change must
keep.

## License

[Apache License 2.0](LICENSE). Copyright 2026 Trevin Tindall (T-Crypt), who created Berth and
maintains it here. Forks are welcome: keep the [NOTICE](NOTICE) file and the copyright header in
each source file, mark the files you change, and release your fork under its own name. The
Berth name, the Pilot mascot and the artwork in `assets/` are not covered by the license.
Third-party components: [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).
