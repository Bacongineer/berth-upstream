# Hermes Agent

[Hermes Agent](https://github.com/nousresearch/hermes-agent) runs around the clock and talks to any
OpenAI-compatible endpoint. Point it at the Berth gateway and every request Hermes makes takes its turn
on the card: the gateway starts the model's engine under a lease, drains whatever holds the card, and
streams the answer.

## Point Hermes at the gateway

Run `berth gateway` (it listens on `127.0.0.1:9799`) with an engine whose `serves` list names the model
Hermes asks for. [Gateway](gateway.md#switch-engines-by-naming-a-model) shows the engine entry.

In `~/.hermes/config.yaml`, use a custom provider. `key_env` names the variable that holds the key, so
the key never sits in the file:

```yaml
model:
  default: router-strata          # a model name from the engine's serves list
  provider: custom
  base_url: http://127.0.0.1:9799/v1
  key_env: BERTH_API_KEY
```

Then set the variable where Hermes starts:

```sh
export BERTH_API_KEY="$(berth key)"
```

## Keep auxiliary tasks on the main model

Hermes sends side tasks (context compression, vision, session titles, skill search) to auxiliary
models. By default every auxiliary task goes to the main model, so it reaches the engine that already
holds the card. Keep that default. If you point an auxiliary task at a second local model behind the
same gateway, each of those calls swaps the card to that model and back.

An auxiliary task can go to an engine that needs no card instead, such as a llama-server on the CPU, by
giving it its own `base_url`. That call never touches the card.

## Cold starts

The first request after a switch waits for the engine to load, which can take a minute or more. A
streamed request keeps its connection: the gateway sends SSE headers at once and a comment every 10 s
until the engine answers. A non-streamed request needs a read timeout longer than the load. To load the
model before Hermes's first request, call the wait endpoint:

```sh
curl -s "http://127.0.0.1:9799/v1/berth/wait?model=router-strata" -H "Authorization: Bearer $(berth key)"
```

Give the engine a `fallback` (a CPU server, for example) and a request that has waited `fallback_after`
goes there while the card loads. [Gateway](gateway.md) covers both settings.

## Teach Hermes to ask for the card

```sh
berth skill install --target hermes
```

This writes the Berth skill to `~/.hermes/skills/mlops/berth/SKILL.md` (under `$HERMES_HOME` when it is
set). Hermes loads it in its next session. The skill tells it to ask Berth before it starts, stops or
switches anything that uses the GPU, instead of killing the process that holds it.
