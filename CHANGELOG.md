# Changelog

Every notable change to Berth is recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); each tagged version
has a matching GitHub release whose body comes from its section below.

## [Unreleased]

### Added

- `berth reclaim [LEASE] [--yes]`: lists the processes a stuck lease recorded
  and, only with `--yes`, stops exactly those (SIGTERM, then SIGKILL after
  `--grace`), re-checking PID and start time before every signal. Processes
  no lease recorded are listed as report only.
  [#48](https://github.com/T-Crypt/berth/pull/48)
- Every descendant of an engine's wrapper is recorded each tick (PID plus
  start time), so a child that escapes its group and reparents keeps the
  lease stuck under its own name instead of showing as a stowaway.
  [#48](https://github.com/T-Crypt/berth/pull/48)
- `contrib/chaos`: `hog.py` and the failure scenarios anyone can run, a drain
  report issue form, and a tested-hardware table in the README.
  [#44](https://github.com/T-Crypt/berth/pull/44)
- `contrib/bench/swap`: times model switches through the gateway (cold and
  warm, per engine), with VRAM after drain, RAM, identical-output and leak
  checks. [#47](https://github.com/T-Crypt/berth/pull/47)
- The engines' VRAM footprints: a freed lease folds its measured peak into
  its engine's record (the most memory seen, the last run's peak, the run
  count), kept in `~/.local/state/berth/footprint.json` across restarts.
  `berth status` shows the last run's peak as EST. VRAM beside the
  engine's own figures, and the JSON names it `estimated_vram_bytes`; it
  is an estimate, never a reservation. A run that holds far more than the
  learned figure (1.5x, after three runs) logs an `engine.footprint`
  event.
- `drain_max` (berth.toml) and `berth run --drain-max DURATION`: a ceiling
  on how long a drain waits at a boundary (a busy slot, a running ComfyUI
  job) before it runs the normal stop ladder; 0 = unbounded, the default.
  While a boundary is busy the wrapper prints a line each minute naming
  the slots and the wait age, and the ceiling says so when it ends the
  wait.

### Changed

- One ComfyUI client, one atomic file write that fsyncs the file and its
  directory, and one `/slots` rule, shared across the daemon.
  [#45](https://github.com/T-Crypt/berth/pull/45)
- Releases publish their CHANGELOG section as the release notes.
  [#44](https://github.com/T-Crypt/berth/pull/44)

### Fixed

- A released lease frees only on a tick whose card read came after the
  release and shows none of its processes, never inside the release itself.
  [#48](https://github.com/T-Crypt/berth/pull/48)

## [v0.2.0] - 2026-10-19

### Added

- Holds and claims: games and other apps get the card while they run, and
  Steam, Lutris and gamescope are covered out of the box.
  [#22](https://github.com/T-Crypt/berth/pull/22)
- The Windows node, experimental: process attribution, claims and
  job-object drains work today. [#23](https://github.com/T-Crypt/berth/pull/23)
- `berth skill install`: an agent skill that teaches coding agents to ask
  Berth for the card instead of killing GPU processes.
  [#24](https://github.com/T-Crypt/berth/pull/24)
- A reader for voice engines, starting with Kokoro-FastAPI.
  [#25](https://github.com/T-Crypt/berth/pull/25)
- `berth pull hf:ORG/REPO` and `berth verify`: Hugging Face downloads with
  a resume, checked byte for byte against the size and sha256 the Hub
  publishes. [#26](https://github.com/T-Crypt/berth/pull/26)
- `berth models`: the model library on disk, GGUF headers, and the
  `berth models export ollama` conversion.
  [#28](https://github.com/T-Crypt/berth/pull/28)
- The gateway's wake page: a bookmark to a stopped engine's web UI that
  shows who holds the card and starts it, plus `drain_slots` so servers
  with `/slots` hand off at their request boundary.
  [#30](https://github.com/T-Crypt/berth/pull/30)
- Shelves for models (chat, image, audio), `berth storage` with each root's
  filesystem and free-space floor, and `berth pull civitai:VERSION_ID` with
  the site's own hashes. [#33](https://github.com/T-Crypt/berth/pull/33)
- The ComfyUI suite commands: `berth engine install`, `update`, `show`,
  `rollback` and `remove`, with the git-pinned components and the trailing
  model-roots rewrite. [#34](https://github.com/T-Crypt/berth/pull/34)
- The gateway's gui plane index, every engine with its state and its launch
  button. [#35](https://github.com/T-Crypt/berth/pull/35)

### Changed

- The README leads with what Berth does and documents the model library.
  [#27](https://github.com/T-Crypt/berth/pull/27)
- Fresh visuals: an animated banner, a how-it-works diagram and a shorter
  first screen. [#31](https://github.com/T-Crypt/berth/pull/31)
- The harbor interface polish: one head rhythm, one KPI grid on every
  screen, touch-sized phone layout and a documents-first nav.
  [#32](https://github.com/T-Crypt/berth/pull/32)

### Fixed

- `berth run` reads an engine's settings from berth.toml, and a flag set by
  hand still wins. [#36](https://github.com/T-Crypt/berth/pull/36)

## [v0.1.0] - 2026-10-11

Berth v0.1.0: the VRAM leasing service for every GPU you run. Leases with
priorities, the measured release (a grant only after the driver shows the
memory free), stuck and contested detection, unprotected runs, the event
log, the harbor web UI and the first engine readers.
