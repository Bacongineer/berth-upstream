---
name: berth
description: Use when working on a machine that runs Berth, the VRAM leasing service for NVIDIA GPUs - before starting, stopping or switching any inference engine, ComfyUI, game or GPU app; when a model fails to load with out-of-memory; when asked what holds the GPU, to free the card, to pause AI for a game or call, or to add an engine or model to Berth.
---

# Berth

Berth coordinates one GPU between many engines (llama.cpp, llama-swap, Ollama, vLLM, SGLang,
ComfyUI, LM Studio, and others), games and apps. An engine leases the card; when another
asks, the holder is drained and Berth waits until the driver shows the memory free before it
lets the next one in. You never have to guess what is on the card: ask Berth.

## First, look

```sh
berth status            # the card, leases, waiters, stowaways, claims, the board
berth status --format json | jq '.leases, .waiting, .squatters'
berth claims            # games and apps Berth holds the card for, or names
berth engine check -v   # what every engine reports right now
berth events -f         # live: grants, drains, refusals, stowaways
```

If `berth status` says berthd is unreachable, Berth is not coordinating this card: say so and
do not assume any engine is protected.

## Rules for an agent

- **Never kill a GPU process to make room.** Ask Berth for the card instead (below). A process
  holding memory without a lease is a *stowaway*: report it to the user with its PID and name.
- **Never start an engine outside Berth** on a Berth machine. Start it through `berth run` (or the
  gateway), or it can load onto a card that is already full and crash both.
- A **pinned** lease (a hold, a claim, `berth run --pinned`) is never drained. If a request waits
  "held by ... pinned", the user's game or app has the card. Tell them; do not work around it.
- Do not edit berth.toml, systemd units or an engine's config without showing the change and its
  undo first. Run `berth config check` after any berth.toml edit.

## Getting the card

| Need | Command |
|---|---|
| Run an engine under a lease | `berth run --as NAME [--priority batch\|interactive\|foreground] [--needs-mib N] -- CMD ARGS` |
| Run an app that must not be drained (Blender, a training job) | `berth run --as NAME --pinned -- CMD` |
| Free the card for the user (game, call) | `berth hold [--for 2h] [--reason game]` (ends on Ctrl-C) |
| Call a model and let Berth start its engine | the gateway: `http://127.0.0.1:9799/v1`, bearer key from `berth key` |
| Wrap every llama-swap model at once | `berth wire llama-swap --config CFG` (plan), `--write` (apply, keeps a backup) |

`berth run` exits 75 when no berth was granted in time; the reason is on stderr. Priorities:
`foreground` drains `interactive` and `batch`; equal priorities follow "last switch wins" with
the juggle guard; `hold` outranks all of them.

## Claims: work Berth does not run

Games rarely appear in the driver's compute list, so Berth watches the process table. Built-in
rules hold the card while a Steam, Lutris or gamescope game runs on Linux, or a game from a
Steam, Epic, GOG, EA, Ubisoft, Xbox or Battle.net library on Windows. They also *track*
Blender, Resolve, OBS, Kdenlive, Premiere and After Effects: that memory is named, never
reported as a stowaway, and nothing waits on it. Add your own in `~/.config/berth/berth.toml`:

```toml
[[claim]]
name = "training"
cmdline = 'train\.py'     # regex on the command line (Windows: the exe's full path)
action = "hold"           # hold: drain engines while it runs; track: just name its memory
linger = "30s"

[claims]
builtin = true            # false turns the built-in pack off
```

## When a model will not load

1. `berth status`: is something else leased, waiting, or a stowaway?
2. A refusal reason names who holds the card and why the request could not fit
   ("needs 20480 MiB; at most 14336 MiB can be freed; claims hold 10240 MiB").
3. `berth events --format json` after the failure shows the drain and grant sequence.
4. Fix the cause (a smaller quant, `--needs-mib`, a priority, the user's hold ending), never by
   killing the holder.

## Reference

- Config: `~/.config/berth/berth.toml` (`berth config show --format toml` prints the evaluated file)
- Output: `--format human|json|yaml`; results on stdout, diagnostics on stderr; exit 0 done,
  1 validation or refusal, 2 Berth could not do its job, 75 (`berth run`) no berth granted.
- Harbor web UI: `berth harbor`, read-only, loopback by default (`http://127.0.0.1:9798/`).
- Docs: https://github.com/T-Crypt/berth (README, contrib/INTEGRATION.md)
