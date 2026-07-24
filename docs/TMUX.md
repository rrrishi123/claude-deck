# tmux mission control

ClaudeDeck models your live tmux world — every session, window and pane, on
this machine *and* on machines reachable through ssh panes — and knows exactly
which Claude Code session runs in which pane. It also snapshots that world
continuously, so a crash, power loss or reboot never costs you the layout or
the sessions again.

Everything below needs **zero cooperation** from tmux or Claude Code: no
hooks, no pane tagging, no plugins, no config. It works on whatever is already
running.

## The Tmux view

The **Tmux** tab shows the topology as a tree:

```
LOCAL
▣ kosaten1                     (attached)
  1: repos
     kosaten1:1.1  claude  ✳ Recover lost tmux Claude sessions
  2: claude-sessions
     kosaten1:2.1  claude  ✳ Map cherry-pick workflow …
     kosaten1:2.2  claude  ✳ Monitor stage smoke job …
  3: repos
     kosaten1:3.1  ssh → rishi@omarchy
       ⇄ RISHI@OMARCHY                  ← tmux inside the ssh pane
       ▣ kosaten-0                (attached)
         1: pilot
            kosaten-0:1.1  ./pilot
```

- Panes running a Claude session get a **✳ chip** with the session's title,
  linked to everything the deck knows about it.
- **⇄ Follow ssh layers** descends into every ssh pane whose remote end runs
  tmux and renders it nested — layer within layer, to any depth. (Off by
  default: it dials the hosts. Unreachable hosts are cached and not hammered.)
- **focus** selects the pane's window+pane on the target server — at any
  layer — so whatever client is attached jumps there.
- **✕** kills a pane (with confirmation), local or remote, same path.

## How pane ↔ session binding works (no hooks)

Three facts make binding reliable without any cooperation:

1. **tty join** — a pane and the process running in it share a tty
   (`#{pane_tty}` ⇔ the process's `ps` tty). That pins every live `claude`
   process to exactly one pane.
2. **argv uuid** — a resumed session runs as `claude --resume <uuid>`: the
   session identity is right there in the process args. Method: `argv`.
3. **title match** — Claude Code sets the terminal title to the session's
   title, which tmux exposes as `#{pane_title}`. For sessions started fresh
   (plain `claude` in argv), the pane title is matched against the deck's
   session titles in that directory. Method: `title`.

If neither pins it down, the binding degrades honestly: `cwd-unique` (only one
known session in that directory) or `cwd-newest` (best guess, flagged as such).
Every binding carries its method so you know how much to trust it.

This is also why the **Sessions** tab gets better with tmux: running detection
becomes per-pane instead of per-directory, so *n* sessions in one directory
all show as running, each with a `⧉ session:win.pane · window-name` chip.

## Resurrect: surviving reboots and crashes

While the deck service runs (`claude-deck --install-service`), it writes a
topology snapshot **every minute** to `~/.claude/tmux-snapshots/latest.json`,
plus a timestamped copy whenever the topology actually changed (last 100 kept).
A snapshot records, per pane: window name and index, **exact pane geometry**
(tmux layout strings are replayable), cwd, what was running, and — for Claude
panes — the session uuid and whether it ran with `--dangerously-skip-permissions`.

After a reboot or tmux-server death:

```bash
claude-deck --tmux-restore latest    # or a specific snapshot file
```

rebuilds every session: windows recreated with their names at their indexes,
panes split to the saved geometry, cwds restored, and each Claude pane gets
`claude --resume <uuid>` (with the bypass flag if it had one) typed into it.

- **User dimensions travel with the pane**: every `@`-prefixed pane option
  (`@pane_label`, `@agent`, …) is captured in each snapshot and re-applied on
  restore — labels agents set on panes survive the rebuild.
- **Additive, never destructive**: a tmux session whose name already exists is
  skipped and reported, never clobbered.
- **Remote layers are deliberately not restored**: tmux on another machine
  survives that machine's own lifecycle. Recreate the ssh pane and you're back
  inside it.
- Take a snapshot manually any time with `claude-deck --tmux-snapshot`.
- Or restore from the dashboard: **Tmux → ⟲ Restore latest snapshot**.

## Search every prompt ever

The **Search** tab is full-text search (SQLite FTS5) over every prompt you
ever sent, in any session. Indexing is incremental — `history.jsonl` is
append-only, so each refresh reads only what's new.

FTS5 syntax works: `"exact phrase"`, `foo AND bar`, `prefix*`. Hits show the
highlighted snippet, the session's title, git branch and live tmux location,
and a **focus** (running) or **resume** (ended) button.

Built for the many-sessions-one-directory workflow: when everything lives in
`~/repos`, project names tell you nothing — titles, branches, pane locations
and prompt content are how you actually find things.

## The witness: push, not just pull

The deck holds a long-lived `tmux -C` control-mode client, so tmux *pushes*
notifications the moment anything happens — no waiting for the next poll.

- **Structural events** (`window-add`, `layout-change`, `window-renamed`, …)
  trigger a debounced observation: the temporal model converges within about
  a second of reality; the minute tick remains as reconciliation.
- **Activity events** (`%output`) are forwarded as metadata — pane id and byte
  count, never content. An agent that sees activity on a pane it cares about
  runs `capture-pane` itself.
- **Agents subscribe instead of polling**: `GET /api/tmux/feed` is an SSE
  stream, one JSON event per line:

```bash
curl -N localhost:7420/api/tmux/feed
# data: {"at":1784903442501,"kind":"output","arg":"%3","bytes":104}
# data: {"at":1784903443502,"kind":"window-renamed","arg":"@3"}
```

Honest scope: `%output` covers panes of the session the witness is attached
to (structural events are server-wide); remote layers are not witnessed —
they stay polled.

## Edge types: attached vs merely reachable

A nested layer carries `edge` describing how the ssh pane above it relates to
that remote tmux: **`attached`** — this pane's ssh session is *provably* a
client of that server (traced connection-by-connection: the local ssh's
source port → the remote login's `SSH_CONNECTION` → its tty → `list-clients`);
**`reachable`** — the host runs tmux but this pane merely leads there. The
same remote server nested under two ssh panes is now distinguishable. Any gap
in the trace (non-Linux remote, multi-hop chain) degrades honestly to
`reachable`, never to a guess.

## Temporal model: panes as slowly-changing dimensions

A topology response is a *mirror* of tmux, not tmux — so ClaudeDeck never
pretends a mirror is live. Two mechanisms:

**Every response says when it looked.** Each layer carries `as_of` (epoch ms).
The right reading of any answer is "`%7` runs zsh *as of 17:01:29*", never
"`%7` runs zsh". Consistency is split deliberately: writes to the store are
ACID (each observation is one SQLite transaction, WAL journal), while the
store's relationship to reality is BASE — eventually consistent, staleness
bounded by the observation interval and always explicit.

**History is versioned, not overwritten.** The pane is the durable entity
(dimension) — its stable tmux ID is the face that persists. What it *shows* —
command, cwd, window, bound Claude session — are SCD Type 2 attributes:
on change, the old row is closed (`valid_to`) and a new one opened, atomically.
The pane title is the one Type 1 exception (Claude's spinner lives in it;
versioning it would explode). `tmux_observations` records every look, so
"unchanged since T" and "unobserved since T" are distinguishable — absence of
observation is never treated as absence.

Observations happen on the service's once-a-minute tick and on every explicit
`/api/tmux` fetch (with `follow=1`, remote layers are recorded too; unfollowed
layers are left untouched, not closed).

```bash
# what did pane %7 show, over time?
curl 'localhost:7420/api/tmux/history?pane=%257'
#   version: cmd=nvim  valid_from=… valid_to=…      ← closed when nvim quit
#   version: cmd=zsh   valid_from=… valid_to=null   ← current

# the whole world as it was yesterday evening
curl 'localhost:7420/api/tmux/history?at=2026-07-23T20:00:00%2B05:30'
```

"What was running when the machine died" — the question that motivates all of
this — becomes one query at the last `valid_to`-free rows before the crash.

## API

Everything the UI does is plain HTTP on localhost:

```bash
# topology; add ?follow=1 to descend into ssh layers
curl localhost:7420/api/tmux

# run any tmux command at any layer: chain [] = local,
# ["rishi@omarchy"] = the tmux behind that ssh pane, and so on deeper
curl -X POST localhost:7420/api/tmux/exec \
  -d '{"chain":["rishi@omarchy"],"args":["send-keys","-t","%0","ls","Enter"]}'

# rebuild from the newest snapshot
curl -X POST localhost:7420/api/tmux/restore -d '{}'

# full-text prompt search
curl 'localhost:7420/api/search?q="cherry-pick"&limit=20'
```

## Notes & limits

- Remote layers need non-interactive ssh (`BatchMode`), i.e. key auth to the
  host. Hosts that fail are cached for 5 minutes and not re-dialed.
- Field separators in tmux formats are printable (`|~|`), because tmux ≥ 3.6
  sanitizes control characters (a literal tab comes out as `_`). Verified
  against tmux 3.5a (macOS) and 3.6b (Linux) in the same tree.
- `claude --resume` may show Claude Code's own "resume from summary?" prompt
  for large sessions after a restore — that choice is yours to make, per pane.
- Restore uses stable tmux IDs and explicit indexes, so `base-index` and
  `pane-base-index` settings are respected.
