# Windows: checking a node on a real box

`check.ps1` is the operator's checklist for a Windows machine with an
NVIDIA card: it writes one report file with what the
[drain report issue form](https://github.com/T-Crypt/berth/issues/new?template=drain-report.yml)
collects, so a report takes one run instead of a dozen screenshots.

## What it checks

- `berth version`, and `berth service status` when the build has it.
- `nvidia-smi`, the driver's view of the card.
- The card and its processes: what `berth status --format json` measures
  for each process next to the driver's own number, read from the same
  counter Task Manager reads (`\GPU Process Memory(*)\Dedicated Usage`).
  Under WDDM `nvidia-smi` cannot report per-process memory, so the driver's
  column is the check of the node's reading. Processes the driver sees that
  berth does not are listed on their own line: that is the expected gap for
  games and other graphics apps until the Windows probe lands, and the
  unexpected one for a misbehaving engine.
- When you give it a CUDA app, one run of it under `berth run --as hog`,
  its output and its exit code.
- The event log around the run (`berth events --json`, the last 200 lines).
  It is captured before you restart anything.

## What it needs

- Windows 10 or 11, Windows PowerShell 5.1 or later (the built-in one).
  The script uses no external modules.
- An NVIDIA driver with `nvidia-smi` on the PATH.
- `berth` on the PATH, or pass its path with `-Berth C:\path\to\berth.exe`.
- The counter names are the English ones; on a non-English Windows they are
  translated and the driver's column reads unavailable, which the report
  says instead of guessing.

## How to run it

```powershell
powershell -ExecutionPolicy Bypass -File check.ps1
```

With a CUDA app to run under a lease (any app that allocates VRAM, its own
command line in quotes):

```powershell
powershell -ExecutionPolicy Bypass -File check.ps1 -App "C:\cuda\app.exe --flag value"
```

The report is written to `berth-windows-check.txt` beside where you ran it
(`-Report path` to choose). Attach the file to the issue. If you restarted
the node or the driver after the run, the event log section is only what
survived: rerun the script before restarting anything.
