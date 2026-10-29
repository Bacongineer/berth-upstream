# Install

Two ways: hand it to your coding agent, or do it yourself. Both end in the same place: the
daemon running as a user service, your engines wrapped in `berth run`, and the harbor UI on
<http://127.0.0.1:9798/>.

## Option 1: have your agent install it

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

## Option 2: manual

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

On Windows 10 or 11, download `berth-windows-amd64.exe` and `SHA256SUMS` from the same release.
Compare the hash PowerShell prints with the line in `SHA256SUMS`, then save the file as `berth.exe`
in a folder on your `PATH`, such as `%LOCALAPPDATA%\Programs\berth`:

```powershell
(Get-FileHash .\berth-windows-amd64.exe -Algorithm SHA256).Hash.ToLower()
```

To update later, run `berth update`, then `berth service install` again so the tasks start the new
binary.

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

On Windows, `berth service install --harbor` creates Task Scheduler logon tasks under `\Berth\`
that run in your session without admin rights, and starts them. Each task writes its log under the
state directory, and `berth service status` prints where.

Open <http://127.0.0.1:9798/>. Engines on their default ports appear on their own.

**3. Wrap your engines** so they lease the card instead of grabbing it. The pattern:

```sh
berth run --as <name> --priority <batch|interactive|foreground> -- <the engine's command>
```

Per-engine recipes (llama-swap, llama.cpp, Ollama, ComfyUI, vLLM, SGLang, custom) are in
[contrib/INTEGRATION.md](https://github.com/T-Crypt/berth/blob/main/contrib/INTEGRATION.md).

**4. Optional configuration** in `~/.config/berth/berth.toml` (on Windows,
`%AppData%\berth\berth.toml`); validate with `berth config check`. On Windows, berthd's
socket is `%LOCALAPPDATA%\berth\berth.sock`, and its state files and service logs are under
`%USERPROFILE%\.local\state\berth`.

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
shelf  = "image"             # text | image | audio; beats what the files say
shared = "windows"           # the other side of a shared root, shown by berth storage
```

**Uninstall:** `systemctl --user disable --now berthd berth-harbor`, remove the two unit
files and `~/.local/bin/berth`, and take `berth run ... --` off any engine command you
wrapped. History lives in `~/.local/state/berth/`.
