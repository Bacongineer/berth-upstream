# The Berth lend protocol

A lend lets an engine give part of its memory on one card to another engine while it keeps serving, and take
it back later. An engine with a cache it can shrink, such as an expert cache or a KV pool, runs slower while
it lends and never reloads. Project Maya serves these routes, and Strata's `/v1/vram` is built into Berth.

This document is the contract for engine authors. The engine keeps no ledger of who borrows: Berth does.
Berth sends every body as JSON with `Content-Type: application/json`, and the engine's key as a Bearer token
when the entry sets `api_key_env` or `api_key_file`.

```toml
[[engine]]
name     = "maya"
type     = "custom"
url      = "http://127.0.0.1:9000"
lend     = "hook"
lend_url = "http://127.0.0.1:9000"
```

Berth does not call these routes yet. The broker side, which decides when to ask for a lend, ships next.

## The three endpoints

A card is named by its PCI address in the `gpu` field. Case does not matter, and the nvidia-smi form
`00000000:09:00.0` is accepted as well as `0000:09:00.0`.

### `POST /vram/lend`

Body: `{"gpu": "0000:09:00.0", "mib": 8192}`.

| Answer | Body | Meaning |
|---|---|---|
| 200 | `{"id": "1", "gpu": "0000:09:00.0", "mib": 8208}` | Lent. `mib` is what the engine gave, which may be more than the ask. |
| 409 | `{"max_mib": 10320}` | Too big. Nothing was lent; `max_mib` is the most the engine can lend on that card now. |
| 503 | `{"error": "..."}` | No answer in time. The engine may still lend, and must list the lend in `/vram/state` if it does. |

Answer 200 only once the engine has let go of the memory. Berth still waits for a driver read that shows it
free before it starts the borrower, so an early answer only costs the borrower a failed start.

### `POST /vram/return`

Body: `{"id": "1"}`.

| Answer | Body | Meaning |
|---|---|---|
| 200 | `{"id": "1", "gpu": "0000:09:00.0", "mib": 8208}` | The engine took the memory back. |
| 409 | `{"error": "...", "retry": true}` | The memory is still in use. It stays lent; Berth tries again later. |
| 404 | `{"error": "no open lease 1"}` | Nothing is lent under that id. Berth treats this as returned. |

Berth asks for a return only after the borrower has stopped and the driver shows its memory gone.

### `GET /vram/state`

```json
{"gpus":   [{"gpu": "0000:09:00.0", "lendable": 10320, "lent": 8208}],
 "leases": [{"id": "1", "gpu": "0000:09:00.0", "mib": 8208}]}
```

`leases` lists every open lend. After a berthd restart, or a 503 on a lend, this is the read that tells Berth
what is still lent.

## Idempotency

A return of an id that is not open answers 404, and Berth counts it as done, so a retried return is safe. A
lend that answered 503 and landed later is returned by its id once `/vram/state` lists it.

## Strata

Strata has one reserve per server, not lend ids. With `type = "strata"` and `lend = "hook"` and no `lend_url`,
Berth calls `POST /v1/vram {"reserve_mib": N}`, where N is the card's free memory plus the amount to lend,
and `{"reserve_mib": null}` to take it back. The server must start with `--vram-elastic` (`"vram_elastic":
true` in its config). A 409 while a request runs means not now, and a 400 means the server cannot resize.
