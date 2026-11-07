# Ahub Workspace Layouts

Status: T0-T3 done. Layouts and `dot hub` shipped in #350 (v2.76.0); Git/Files windows, the opt-in menu and the T3 receipts in the #331 follow-up.
Date: 2026-11-07
Issue: https://github.com/STAIxBWLB/dotfiles/issues/331

## Problem

`dot open` builds Claude-centred tmux layouts. A person who works through
agent-hub (ahub) wants the hub console in view, the peers it coordinates beside
it, and a way back to the console from a session that predates these layouts.
dotfiles owns the layout and tmux navigation; ahub owns the console, peers,
tasks, approvals and recovery. No ahub change is needed.

## Decisions (T0)

Layouts

- Two new layouts, `ahub` and `ahub-compact`, selected only by `dot open --layout`
  or `dot register --layout`. `default_layout` stays `dev`, and dot has no command
  that rewrites it, so no project's effective layout changes.
- `ahub`: window `work` with AHUB on the left at 40% of the columns but never
  under 80, PEER A over PEER B (60/40) on the right, plus a window `verify` with a
  project shell. Git, files and the monitor are not permanent panes.
- `ahub-compact`: one full-size window each for `hub`, `peer-a`, `peer-b`, `verify`.
- A new `ahub` session in a terminal under 200x56 becomes `ahub-compact` and says so.
- The ahub layouts create the session at the terminal's size (`stty size` in the
  launcher), minus the status line rows, because a detached session does not
  subtract the status line until a client attaches. The legacy layouts keep
  their fixed 220x60.

Measured on an isolated tmux 3.8 server (`TestAhubLayoutGeometry`):

| Terminal | Layout | AHUB pane | Console |
|---|---|---|---|
| 220x60 | ahub | 88x59 | full (`--panels`) |
| 200x56 | ahub | 80x55 | full |
| 160x45 | ahub → compact | 160x44 | full |
| 100x30 | ahub-compact | 100x29 | full |
| 80x25 | ahub-compact | 80x24 | full |
| 80x24 | ahub-compact | 80x23 | plain `ahub console` |

Slots and startup

- Every slot runs `ahub-pane.sh`, deployed with the other workspace scripts. It
  starts nothing by itself. AHUB opens `ahub --project <root> console` only when
  `ahub --project <root> status --json` succeeds; `--panels` needs a pane of at
  least 80x24 at the time it opens, else the plain console. When the hub is down
  it names the reason and starts `ahub --project <root> up --no-agents` only on
  Enter, so the configured default peers are not launched outside the slots.
- Peer slots: the first two usable ids of `start.agents` in the project's
  `.agenthub/config.local.json`, then `codex`, then `claude`. An id is usable when
  it is a kind ahub starts by name (claude, codex, kimi, jcode, qwen, pi, local),
  launched as `ahub --project <root> <kind>`, or an instance declared there with
  one of those kinds, launched as `ahub --project <root> <kind> --as <id>`. Ids
  must match `[A-Za-z0-9_-]+`. The file only chooses among these fixed forms;
  nothing in it is run. A slot shows its command and starts the peer on Enter.
- A peer already attached elsewhere is left to ahub's own refusal; the slot never
  takes over, stops or replaces it. Slot labels are views, not peer authority.
- Resume never rebuilds: `dot open` on a live session attaches it, and a
  `--layout` given for it is reported as not applied.

Workspace action: `dot hub <project> [console|status|ui|git|files]`

- Resolves the registered project and its root; an unregistered name is an error,
  never an auto-registration.
- `status` and `ui` run `ahub --project <root> status|ui` in the current terminal.
- `console` outside tmux runs `ahub --project <root> console` in the current terminal.
- `console` inside tmux acts only in the project's session, identified by session
  name and by `session_path` resolving to the registered root. It focuses the pane
  tagged `@dot_slot=hub`, or adds one `hub` window running `ahub-pane.sh hub` to a
  session without one. A `tmux wait-for` lock per session serializes concurrent
  calls. Any other session gets the switch command and the plain ahub command.
- `git` and `files`, inside that session only, work like `console`: they focus
  the pane tagged `@dot_slot=git|files`, or add one `git`/`files` window that runs
  the existing `tools.sh` launcher (`run_lazygit`: lazygit, else `git status`;
  `run_filetree`: yazi, eza, tree, else `ls`). Outside tmux they print guidance.
- The caller's session is read from `$TMUX_PANE` when set. Under `run-shell`
  (a key binding) there is no client, and an untargeted `display-message`
  names the most recent session, not the caller's.
- It never starts the hub or a peer, never relayouts, and never enters the
  launcher's kill, retry or socket-cleanup paths.

Opt-in menu: `modules.workspace.ahub_keys: true`

- Renders `prefix+A` (unbound in the template and in tmux's defaults) as a
  `display-menu`: Hub console, Git, Files, and Status (a popup). Each entry runs
  `TMUX_PANE=#{pane_id} dot hub #{q:session_name} ...`; `#{q:}` quotes the
  session name for the shell. Off by default; turning it on adds lines and
  changes none (`TestTmuxConfAhubKeysOptIn`).

Ownership marks: session option `@dot_root` and pane option `@dot_slot`
(`hub`, `peer-a`, `peer-b`, `verify`). They live in the tmux server only.

## T3 receipts (2026-11-07)

Real PTY clients on isolated tmux 3.8 servers (own socket, fake HOME, a fake
ahub whose hub is running, `bash --norc`). The session is built by the
launcher's `load_layout` at the client's size; the launcher itself was not run,
since it cleans the person's `/tmp/tmux-<uid>` sockets.

R1, attached-client resize (local, `ahub` created at 220x60): pane ids and
processes unchanged at every size.

| Client | AHUB | PEER A | PEER B |
|---|---|---|---|
| 220x60 | 88x59 | 131x35 | 131x23 |
| 160x45 | 58x44 | 101x27 | 101x16 |
| 100x30 | 28x29 | 71x19 | 71x9 |
| 80x24 | 18x23 | 61x16 | 61x6 |
| 230x62 | 93x61 | 136x35 | 136x25 |

A small client on a wide session shrinks AHUB below 80x24; native zoom
(`C-a z`) gives it the whole client: 100x29 at 100x30, 80x23 at 80x24. The
console keeps the mode it opened with until reopened (Ceilings).

R2, `prefix+A` menu (local): `g` added a `git` window and focused it, `f` a
`files` window; `g` again focused the same window (one `git` window); `a`
focused AHUB in `work`. The original four panes stayed.

R3, SSH reattach (macbook-pro--2023, tmux 3.8): a client attached over SSH at
220x60, the ssh process was killed (a dropped connection), the server listed no
client, and a new SSH client attached at 120x40. Pane ids and processes were
unchanged; AHUB 38x39, PEER A 81x25, PEER B 81x13.

Journeys (AC10), counted from the slot behaviour above, to reach the hub console
plus two peers running under ahub, for a registered project:

| | `ahub` layout | `dev` layout |
|---|---|---|
| Warm (hub running) | `dot open`; console opens by itself; per peer: focus its slot, Enter. 1 entry + 2 actions, 0 for the console | `dot open` starts standalone Claude; quit it, then type `ahub console`, `ahub codex` and `ahub claude` in new panes or windows. 1 entry + 4 actions |
| Cold (hub down) | as warm, plus Enter in AHUB for `ahub up --no-agents`: 1 entry + 3 | as warm, plus `ahub up --no-agents`: 1 entry + 5 |

Native sign-in, trust and approval prompts come on top of either and were not
bypassed. These are counts of explicit actions, not timed sessions.

## Deferred

- A status projection inside dotfiles; `dot hub <project> status` runs ahub's
  own, and dot only reports a missing `ahub` (AC7 as decided).

## Ceilings

- A `dot hub` killed while holding its lock leaves `dot-hub-<project>` locked;
  `tmux wait-for -U dot-hub-<project>` clears it.
- The console mode is chosen when AHUB opens it; a later resize keeps the mode
  until the console is reopened.
