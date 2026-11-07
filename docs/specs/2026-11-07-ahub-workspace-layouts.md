# Ahub Workspace Layouts

Status: T0 decisions frozen and T1/T2 slices implemented on `feat/331-ahub-layouts`; T3 client and journey receipts pending.
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

Workspace action: `dot hub <project> [console|status|ui]`

- Resolves the registered project and its root; an unregistered name is an error,
  never an auto-registration.
- `status` and `ui` run `ahub --project <root> status|ui` in the current terminal.
- `console` outside tmux runs `ahub --project <root> console` in the current terminal.
- `console` inside tmux acts only in the project's session, identified by session
  name and by `session_path` resolving to the registered root. It focuses the pane
  tagged `@dot_slot=hub`, or adds one `hub` window running `ahub-pane.sh hub` to a
  session without one. A `tmux wait-for` lock per session serializes concurrent
  calls. Any other session gets the switch command and the plain ahub command.
- It never starts the hub or a peer, never relayouts, and never enters the
  launcher's kill, retry or socket-cleanup paths.

Ownership marks: session option `@dot_root` and pane option `@dot_slot`
(`hub`, `peer-a`, `peer-b`, `verify`). They live in the tmux server only.

## Deferred

- Opt-in key bindings for the hub menu or focus (prefix+H is taken); `dot hub`
  covers the action meanwhile.
- On-demand Git and Files windows from the ahub layouts.
- A status projection inside dotfiles; `dot hub <project> status` runs ahub's own.
- T3 receipts: attached-client resize, SSH reattach, and the warm/cold journey
  counts against the dev layout (#331 AC10).

## Ceilings

- A `dot hub` killed while holding its lock leaves `dot-hub-<project>` locked;
  `tmux wait-for -U dot-hub-<project>` clears it.
- The console mode is chosen when AHUB opens it; a later resize keeps the mode
  until the console is reopened.
