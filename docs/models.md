# Models

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
- **Shelves.** `berth models` reads each model for the kind of engine that loads it: text,
  image, audio, or unshelved when the file states nothing. Text is what llama.cpp, vLLM or
  Ollama loads: chat models, and also embedders, rerankers and vision or OCR models. Image
  is a diffusion model for ComfyUI or a similar engine, audio a speech model. Before v0.3.4
  the text shelf was called `chat`, and `shelf = "chat"` and `--shelf chat` still work. A root declared in `[library]` (say
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
