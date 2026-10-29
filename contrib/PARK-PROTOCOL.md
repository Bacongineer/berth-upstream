# The Berth park protocol

Berth moves an engine's weights to system RAM on a drain, so the next use
resumes in seconds instead of reloading the model from disk. ComfyUI, vLLM
and SGLang park through the adapters built into Berth. Any other engine
can be parkable too: serve three endpoints on the engine's own HTTP API,
and Berth finds them. An engine that cannot serve them, such as llama.cpp or
NInfer, parks through NVIDIA's `cuda-checkpoint` instead and needs no protocol
([Park in RAM](https://github.com/T-Crypt/berth/blob/main/docs/gateway.md#park-in-ram-warm-switch)).

This document is the contract for engine authors. Implement it, point an
engine entry at your server, set `park = "hook"` and no URLs:

```toml
[[engine]]
name = "llamacpp-qwen"
type = "llamacpp"
url  = "http://127.0.0.1:8081"
park = "hook"
```

and Berth parks your engine through the protocol on every drain. An engine
that does not serve the protocol drains exactly as it does today: nothing
breaks, it just reloads.

## The three endpoints

All three live under the engine's base URL, the same one Berth reads its
metrics from. Berth sends no request body and no headers of its own on any
of them.

| Call | The engine answers | Meaning |
|---|---|---|
| `GET /berth/state` | `200` and `{"state": "hot"}` | which of the four states the engine is in, always |
| `POST /berth/park` | `200` when the weights are off the card | move the weights to system RAM |
| `POST /berth/resume` | `200` when the weights are back and the engine serves | move them back to the card |

The four states, in the order one park and one resume walk through them:

| State | The weights | The engine |
|---|---|---|
| `hot` | on the card | serving |
| `parking` | on the card, an offload is under way | not serving |
| `parked` | in system RAM | holding only its residual |
| `resuming` | a restore is under way | not serving |

"Only its residual" is the small amount of VRAM an engine keeps while
parked: a CUDA context, a KV cache it chose to keep, runtimes. Berth
measures it on the driver and admits the next engine around it while the
residual stays within `park_residual_max` (2 GiB by default); above that
the parked engine is evicted.

## Timing

- Berth makes the two calls at a boundary, when the engine is idle: no
  prompt is running, no job is in flight.
- `POST /berth/park` should return `200` once the weights are off the
  card, not once the offload is merely started. Berth then waits for a
  card read to show the residual before it releases the lease: the
  driver's reading, not the engine's word, is what frees the card. If
  the card is still full when the release grace (30 s) runs out, Berth
  stops the engine the normal way, so a park that does not free the card
  never strands the next engine.
- `POST /berth/resume` should return `200` once the weights are on the
  card and the engine can serve. Berth then talks to the engine through
  its normal API, as for any start.
- `GET /berth/state` should be cheap and correct at all times, including
  mid-way (`parking`, `resuming`). Berth reads it once, within three
  seconds, when it detects the protocol; a slow or broken answer simply
  reads as "not the protocol".

## Idempotency

A duplicate call must not break the engine:

- `POST /berth/park` when already `parked` answers `200` and does nothing.
- `POST /berth/resume` when already `hot` answers `200` and does nothing.

Berth calls each side once per park or resume, but drains retry and two
clients can race a handoff, so the no-op answers are part of the
contract.

## Errors

- `GET /berth/state` answering `404` is how an engine says "I do not
  implement the protocol". Any non-`200` answer, or a `200` whose body is
  not `{"state": <one of the four states>}`, means the same: Berth parks
  the engine no differently from today.
- A non-`200` from `POST /berth/park` means the offload failed. Berth
  continues with the normal drain (SIGTERM, then SIGKILL, as for any
  engine) and reports the park failure. If your engine cannot offload in
  some situations, answer `409` or `500` there instead of `200`.
- A non-`200` from `POST /berth/resume` means the engine is still
  parked; the request for it waits or is refused under the normal lease
  rules, and the failure is in the event log.

Two rules keep the protocol safe to run:

- Never leave the card holding weights you told Berth were off it. A `200`
  from park is a promise the driver will check.
- Never drop the weights to free RAM. The parked copy lives in system RAM
  until Berth evicts the engine or it restarts, and a resume has to
  restore what a park moved.

Authentication rides on whatever the engine's API already has. Berth
sends no credentials: if the engine's endpoints require them, the
protocol paths must either be open on the engine's local port or the
engine must serve them without, since Berth has no key to give.

## A minimal server

Python, standard library only. The two marked lines become a real
engine's code for moving weights to system RAM and back.

```python
#!/usr/bin/env python3
"""A minimal Berth park protocol server (stdlib only). OFFLOAD and
RESTORE become a real engine's weight-to-RAM and back code."""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

STATE = "hot"  # hot | parking | parked | resuming

class Engine(BaseHTTPRequestHandler):
    def _reply(self, code, body=None):
        self.send_response(code)
        self.end_headers()
        if body is not None:
            self.wfile.write(json.dumps(body).encode())

    def do_GET(self):
        if self.path == "/berth/state":
            self._reply(200, {"state": STATE})
        else:
            self._reply(404)

    def do_POST(self):
        if self.path == "/berth/park":
            global STATE
            if STATE == "hot":
                # OFFLOAD: move the weights to system RAM
                STATE = "parked"
            self._reply(200)
        elif self.path == "/berth/resume":
            if STATE == "parked":
                # RESTORE: move the weights back to the card
                STATE = "hot"
            self._reply(200)
        else:
            self._reply(404)

HTTPServer(("127.0.0.1", 8199), Engine).serve_forever()
```

It is 37 lines. Run it where an engine would sit, point a `park = "hook"`
engine entry at `http://127.0.0.1:8199`, and `berth status` shows it
parking and resuming like any adapter-backed engine.
