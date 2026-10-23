# Leasing, games and holds

## Leasing (the core)
- **Explicit leases.** Engines start through `berth run`, which waits for a berth, starts
  the engine in its own process group, and gives the berth back when the engine exits.
- **Priorities.** `foreground` > `interactive` > `batch`. An equal or higher request
  asks the holder to drain; a lower one waits and is refused with a reason naming the
  holder (exit 75), never an out-of-memory crash.
- **Handoff in steps.** A drain tries the engine's own unload first (for llama-swap, its
  unload URL), then SIGTERM to the process group, then SIGKILL, each after a grace period.
- **Measured release.** Berth grants the next engine only after the driver shows the old
  holder's processes gone, since an exited process can still hold memory.
- **Stuck releases are caught.** A released lease whose memory never frees turns
  *aground* and keeps blocking, with a reason naming the process and how much it holds.
- **A way out for a stuck release.** `berth reclaim [lease]` lists what the stuck lease
  recorded, PID, start time, memory and command, and anything on the card no lease
  recorded ("not Berth's", report only). With `--yes` it stops exactly the recorded
  ones, SIGTERM first and SIGKILL to the survivors after `--grace`, re-checking each
  PID and start time immediately before every signal.
- **Process identity is PID plus start time**, so a reused PID never inherits a lease.
- **Children stay on the lease.** On Linux with a systemd user manager, `berth run`
  gives the engine its own cgroup scope, which every child inherits on fork: a child
  that escapes its process group and reparents keeps the lease stuck under its own
  name instead of turning into a stowaway. Without a user manager, and on Windows,
  Berth records every descendant of the wrapper each tick, which covers everything
  except a fork whose parent exits inside one tick.
- **Whole-card or sized leases.** `--needs-mib N` lets engines that fit share a card.
- **Fail-open, never silent.** If the daemon is down, `berth run` still starts the engine
  but records it as an *unprotected run* that status shows (`--fail-closed` refuses).
- **Survives a daemon restart.** A running engine stays up when berthd restarts or is
  upgraded; `berth run` dials the new daemon and re-attaches by PID plus start time. For
  15 s after a start the daemon grants nothing new while such engines are still due back,
  so nothing is loaded onto a card an engine already holds.
- **ComfyUI at a job boundary.** `--drain-comfyui` waits until ComfyUI's queue is empty
  before stopping it; a job in progress is never cut off.
- **No juggling.** Two engines at the same priority drain each other: the last one asked for
  wins, which is what you want when you switch models. When two clients want different
  engines at once, that flips the card back and forth and every request fails. Berth calls it
  a juggle when an engine is drained within `juggle_window` (15 s) of getting the card while it
  is still busy (a request in flight, a ComfyUI job queued, a model loading). The engine ranking
  decides a juggle: the winner keeps or takes the card, and the loser waits for it instead of
  draining it back. An idle holder hands over at once, so chat, then an image, then chat again
  is three clean handoffs. A person's switch (Launch on the gateway's wake page, or
  `/v1/berth/switch`) is never a juggle: it takes the card whatever the ranking says, and inside
  the window a request no person made (a chat client's background call) does not take it back.
  To keep it longer, give the engine a `min_turn`.

  ```toml
  [priority]
  order = ["llama-swap", "strata", "comfyui"]  # first is best; omit to rank by use
  juggle_window = "15s"                        # a negative value turns it off
  ```

  Without an `order`, the engine with the most hours on the card through Berth over the last
  7 days ranks first (kept in `~/.local/state/berth/usage.json`); a tie leaves the holder in
  place. `berth run --group NAME` files a lease under an engine family: llama-swap runs each
  model under its own name, so give each `--group llama-swap`. `berth status` shows the ranking
  and a count of juggles stopped; the harbor log shows each one with the reason.

  - **A minimum turn per engine.** `min_turn = "5m"` on the engine entry (or
    `berth run --min-turn 5m`) gives the engine a turn it keeps for that long after the
    grant: in that time a request of equal or lower priority cannot drain it, and neither can
    a higher one, because a request interrupted halfway is a lost one. Only a hold (a game, a
    call, `berth hold`) takes the card out before the turn ends.

    ```toml
    [[engine]]
    name  = "comfyui"
    min_turn = "5m"   # the card stays this engine's for 5m after the grant
    ```

    A blocked waiter is told how much of the turn is left ("next in line: comfyui has its
    minimum turn, 3m12s left"), and `berth status` shows the time a lease has left in the
    TURN column. When the turn ends the normal rules apply: a drain still starts at the slot
    or job boundary, and a minimum turn never cuts a request off. After a daemon restart, a
    re-attach starts a fresh turn.

## Games, apps and holds
- **Claims cover work Berth does not start.** berthd scans the process table every 2 s.
  A *hold* claim keeps the card while its process runs: engines are drained and wait, with a
  15 s linger so a launcher restarting the game does not bounce the card back. A *track*
  claim names an app's card memory so it counts against the card without being reported as a
  stowaway; nothing waits on it.
- **Built-in pack, on by default.** Holds: Steam games (anything under Steam's `reaper`),
  Lutris and gamescope on Linux; Steam libraries (`steamapps\common`), Epic, GOG, EA,
  Ubisoft, Xbox and Battle.net games on Windows, with launchers, Wallpaper Engine and
  Lossless Scaling left out. Tracks: Blender, DaVinci Resolve, OBS, Kdenlive, Premiere,
  After Effects. `berth claims` shows every rule and what it matches now.
- **Your own rules:**

  ```toml
  [[claim]]
  name = "training"
  cmdline = 'train\.py'      # regex; on Windows it sees the exe's full path
  action = "hold"            # or "track"
  linger = "30s"

  [claims]
  builtin = false            # turn the built-in pack off
  ```

  A rule can also match `exe` names (globs, `.exe` optional), a launcher `parent` (with
  `parent_cmdline`, a regex that launcher's own command line must match too), and exclude with
  `not_cmdline`. The built-in Steam rule matches Steam's `reaper` only when it runs as
  `reaper SteamLaunch AppId=...`, so the REAPER DAW, also named `reaper`, holds nothing.
- **`berth hold [--for 2h] [--reason game]`** takes the card for you. It is a pinned lease at
  the top priority, so nothing drains it, and it ends on Ctrl-C, after `--for`, or when the
  process dies.
- **`berth run --pinned`** gives an app a lease that is never drained (it keeps the card until
  it exits).
- A hold cannot free memory held by something outside Berth: that memory is still reported,
  as contested.
