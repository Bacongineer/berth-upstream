# chaos: try to break Berth

`contrib/chaos` is a test kit for Berth's failure handling. An engine that
holds VRAM can misbehave in a few specific ways: ignore the polite stop, run
away from its process group, keep saying it is busy, or exist without a lease.
These scenarios reproduce each one with a small PyTorch program, so you can
check on your own hardware what Berth does, before or after filing an issue.
If you were asked in an issue to run one, this is that place.

## What you need

- One NVIDIA card, with `nvidia-smi` working.
- Python 3 with PyTorch (any version). `hog.py` allocates real tensor memory
  on cuda:0 and holds it.
- Berth installed, and `berth daemon` running.

hog.py runs until something stops it. After a scenario, end it yourself:
`pkill -f hog.py` for the ones already gone, and SIGKILL what survived a
drain (`kill -9` the PID from hog.py's own output).

## hog.py

```sh
python hog.py GIB MODE       # MODE: polite | ignore-term | escape-child | busy-slots
```

| Mode | What it does |
|---|---|
| polite | Holds GIB GiB and exits on SIGTERM. |
| ignore-term | Holds GIB GiB and ignores SIGTERM. |
| escape-child | Forks a setsid child that holds the GIB GiB and exits itself. |
| busy-slots | Holds GIB GiB and serves `GET /slots` on `--port` (default 18080) with one slot `is_processing` forever. |

## Scenarios

Every command starts from an empty card except where a scenario says so. Run
each `berth` command in its own terminal. `berth events -f` in a third shows
the log live; the expectations below quote its event kinds.

| # | Scenario | Run | Expected |
|---|---|---|---|
| 1 | Clean drain | `berth run --as a -- python hog.py 8 polite`, then, once a is in, `berth run --as b --priority foreground -- python hog.py 8 polite` | a is asked to drain, gets SIGTERM and exits; b is granted only after the driver shows a's 8 GiB gone. The event log gets `lease.draining`, `lease.released`, `lease.freed`, `lease.granted` in that order. |
| 2 | Ignores SIGTERM | as scenario 1 with `ignore-term` on the first run | SIGTERM goes to a's own process group, nothing happens for the 30 s grace, then SIGKILL goes to that same group. No other process is signalled. After a dies, the handoff is as in scenario 1. |
| 3 | Escaped child | as scenario 1 with `escape-child` on the first run; the parent exits immediately, so start b at any time (`berth run --as b --priority foreground -- python hog.py 8 polite`) | Every tick Berth records each descendant of a's wrapper (PID plus start time), so a child that escapes its group and reparents stays a's: once a exits, the lease turns `stuck` after the 30 s release grace and the log names the child's PID and MiB; b waits and is told why. `berth reclaim` lists it, and `berth reclaim --yes` stops exactly that child. Nothing stops it on its own. One window remains: a child that forks and whose parent exits inside one tick (2 s) before any record shows as a stowaway, and a whole-card b is granted `contested` with a warning. |
| 4 | Stowaway | `python hog.py 8 polite` on its own (no `berth run`), then `berth run --as b --needs-mib 20000 -- python hog.py 4 polite` on a 24 GB card | The hog shows up as a stowaway with 8192 MiB in status, the events log and the harbor. b waits with `squatters hold 8192 MiB`; it is never granted while the hog holds memory, and the hog is never signalled. |
| 5 | Unrelated workload survives | run scenario 2 while `glxgears` (or a game) is running | The kill ladder reaches only a's process group: the unrelated window keeps drawing through every step, and the handoff finishes around it. |
| 6 | Busy forever | `berth run --as a --drain-slots http://127.0.0.1:18080 --drain-max 10m -- python hog.py 8 busy-slots`, then `berth run --as b --priority foreground -- sleep 30` | The drain waits on a's `/slots`, which is busy forever. Without `drain_max` b waits without limit; the wrapper prints `draining a: 1 slot busy for 6m12s` each minute, so the journal says why. With `drain_max` (the flag, or `drain_max` in a's berth.toml entry) the wait ends after that long: the wrapper says `a still busy after 10m0s (1 slot processing); draining anyway`, and the drain runs the normal ladder, so b gets the card. |
| 7 | Daemon restart mid-lease | scenario 1; while a holds the card, `systemctl --user restart berthd` (or stop and start the daemon yourself) | a keeps running, its wrapper re-dials and re-attaches within the restart window, and its lease is active again. A b started in between is granted nothing until the re-attach; then it takes the card as in scenario 1. |
| 8 | Containerized engine | `berth run --as comfyui -- python hog.py 8 polite`, then `berth run --as vllm --container vllm --priority foreground -- docker run --rm --name vllm --gpus all -v $PWD:/kit python:3.11-slim python /kit/hog.py 16 polite`, then `berth run --as comfyui2 --priority foreground -- python hog.py 8 polite` | The dockerized hog is never a stowaway: containerd-shim parents it, so the tree cannot reach it, but the lease named the container and Berth attributes its memory to the vllm lease from its first sighting. The drain stops the container with `docker stop -t 30 vllm` (the image's own SIGTERM, then SIGKILL) instead of signalling the wrapper, and the next grant waits for the driver to show the container's 16 GiB gone. A container Berth did not start stays a stowaway, reported by container id, never signalled. |

## Report a run

Results, including the ones where Berth fell over, are welcome as issues.
Use the [drain report issue form](https://github.com/T-Crypt/berth/issues/new?template=drain-report.yml).
It collects: which scenario, pass / fail / other, the GPU and its VRAM, the
driver version (`nvidia-smi` prints it), the OS, the engines on the card,
the berth version (`berth version`) and the event log around the run
(`berth events --json`). Capture the events output before you restart
anything, and paste it whole.
