# Berth documentation

Berth decides who gets your GPU. Engines, games and GPU apps lease the card in turn, so two of them
never load at once and crash each other. The [project README](https://github.com/T-Crypt/berth#readme)
has the overview, the quick start and screenshots. These pages are the reference.

| Page | What it covers |
|---|---|
| [Install](install.md) | The agent prompt, manual install, `berth.toml`, uninstall |
| [Engines](engines.md) | Every engine Berth reads, API keys, ComfyUI as a suite |
| [Leasing, games and holds](leasing.md) | Priorities, drains, stuck releases, the juggle guard, minimum turns, claims and `berth hold` |
| [Models](models.md) | `berth pull`, integrity checks, shelves, `berth storage`, the page cache and `berth prefetch` |
| [Gateway](gateway.md) | One OpenAI-compatible port, cold starts, fallbacks, KV snapshots, switch costs, wake pages, park in RAM |
| [The harbor and telemetry](harbor.md) | Stowaways, the collisions board, alerts, every harbor panel, what Berth measures |
| [Interfaces](interfaces.md) | The CLI's output contract and exit codes, the harbor's options, Prometheus |
| [Benchmarks](benchmarks.md) | Tested hardware and measured swap times |
| [Roadmap](roadmap.md) | Phases, what each release shipped, what comes next |
| [FAQ](faq.md) | Common questions, Docker engines included |

Engine recipes for `berth run` are in
[contrib/INTEGRATION.md](https://github.com/T-Crypt/berth/blob/main/contrib/INTEGRATION.md), and every
release's changes are in the [CHANGELOG](https://github.com/T-Crypt/berth/blob/main/CHANGELOG.md).
