# Library v1 (F3K1CQY): shelves, roots, `berth storage`, in-root catalog, CivitAI pull

Branch `feat/library-v1` off origin/main (8a77678, includes #26 pull/verify, #28 model library, #32 harbor refresh).
No subagents; each unit builds, tests, commits before the next.

## Data shapes

- **Shelf** (`internal/models/shelf.go`): `type Shelf string`, `chat` | `image` | `audio` | `""` unknown.
  Resolution order: config root override > file facts > name hints > unknown. Never a guess.
  File facts: GGUF arch (LLM archs and embeddings and clip/mmproj -> chat; whisper -> audio; SD-family -> image),
  safetensors header tensor names (`lora_`, `diffusion_model.`, `first_stage_model.`/`vae` -> image;
  `language_model.` -> chat), `config.json` model_type, Ollama/LM Studio stores -> chat.
- **Root** (`internal/storage`): `Info(path)` -> `Root{Path, FS, Total, Free, Used, Supports{Hardlink, Symlink, Reflink}}`.
  Linux: `unix.Statfs` f_type magic table (ext4, btrfs, ntfs3, exfat, xfs, overlay, tmpfs, zfs, f2fs; else "unknown").
  Non-linux stub: FS "unknown", zero sizes, no support claims (floor check then warns and is skipped: measured, never assumed).
  Support table is per-FS, static.
- **Floor**: `Root.UnderFloor(need, frac)` = `Free < need + Total*frac`. Config `[library] free_floor` is a
  fraction (0 disables); default 0.10. `berth pull` refuses before downloading when the root would drop under it;
  roots where space cannot be measured warn and proceed (fail open, never a false reservation claim).
- **Manifest** (`internal/manifest`, replaces `internal/hf/manifest.go`): `Manifest{Source, Name, Ref, Files, Pulled}`,
  still written as `.berth-manifest.json` beside the files. Source "hf" | "civitai"; a manifest with no source
  normalizes to "hf" (legacy files on disk). `Verify` unchanged in shape.
- **Catalog** (`internal/models/catalog.go`): `<root>/.berth/catalog.json` = one root's scan snapshot
  `{root, host, at, models:[{name, path, shelf, bytes}]}`. Written best-effort after a scan (never fatal:
  read-only roots skip it); read back by `berth storage` so the sibling boot on a shared root sees what this
  boot resolved. The scan recomputes from files and never depends on the catalog.
- **CivitAI** (`internal/civitai`): `Endpoint()` (env CIVITAI_ENDPOINT), `ParseRef` for `civitai:<version-id>`,
  `civitai:model:<model-id>` (latest version), plain version-id, and `civitai.com/model-versions/<id>` URLs.
  Client: `Search`, `ModelVersions`, `Version` (files carry size, sha256 hash and downloadUrl), `Download`
  through the shared `internal/dl` transfer (part-file resume, size + sha256 verification), token from
  `CIVITAI_API_KEY` via `--token-env`.
- **dl** (`internal/dl`): `Fetch(ctx, FetchSpec{URL, Size, SHA256, PartPath, Token, Prog})` = one resume attempt
  (open part, Range from current size, size check, sha check). hf's retry loop keeps owning its backoff;
  civitai does the same around the same call.

## Config

```toml
[library]
free_floor = 0.10          # fraction of a root's total space that stays free; 0 disables the guard
[[library.root]]
path  = "/mnt/windows/ComfyUI/models"
shelf = "image"            # chat | image | audio; declared shelf beats inference for the whole root
shared = "windows"         # the other OS this root is shared with; "" = local only
```

`[models] dirs` stays the root list; `[library]` adds the knobs. Root overrides match by absolute path.

## Commands

- `berth storage` (new): roots from `--dir`, else `[models] dirs` + `[library] root` paths, else defaults.
  Per root: filesystem, total/free/used with percent, shared-with, hardlink/symlink/reflink, per-shelf
  bytes and model counts (scanned), floor verdict, in-root catalog age and host. Exit 1 when any root sits
  under its floor.
- `berth civitai search QUERY [--type T]... [--limit N]` (new group) and `berth civitai versions MODEL_ID`:
  discovery only; downloading stays in `berth pull`.
- `berth pull civitai:<ref>`: resolves the version, floors the destination root, downloads with resume and
  sha256, writes the shared manifest into `DIR/civitai/<name-slug>/`.
- `berth pull hf:...` gains the same floor guard.
- `berth models --shelf chat|image|audio|unknown`: filter; the listing gains a SHELF column and the footer
  totals per shelf; each scanned root gets its `.berth/` catalog written (best effort). `show` prints the shelf.

## Units (one commit each, proof command green after each)

1. `internal/manifest`: unified manifest + legacy normalization; hf and cli callers move over.
2. `internal/storage`: Root/Info/supports/floor, linux + non-linux files, tests.
3. `internal/models` shelf + safetensors header reader; `Model.Shelf` filled in every scan path; tests.
4. `internal/models` catalog read/write; tests.
5. `internal/config` `[library]` section + validation + floor accessor; tests.
6. `internal/dl` shared transfer; hf refactored onto it (its tests unchanged); resume test.
7. `internal/civitai` client + tests against httptest (search, versions, version, ranged download).
8. `berth storage` command + tests.
9. `berth civitai search|versions` + tests.
10. `berth pull`: civitai dispatch + floor guard on both sources + new manifest; tests.
11. `berth models`/`show`: shelf column, filter, totals, catalog write; tests.
12. README + help text; full proof: build, gofmt, vet, tests, golangci-lint, windows cross-build.

## Open decisions (draft PR will name them)

- Floor guard is a refusal, not a `--force` override: the no-disk-fills rule is absolute (GUIDED-VISION principle 6).
- The catalog is a shared snapshot, not a cache: a stale one is harmless because the scan recomputes.
- CivitAI search needs the API key only for gated material; public search works keyless, same as HF.
