# Workspaces and tmux

## Tmux

### Key Bindings

| Key | Action |
|-----|--------|
| `C-a` | Prefix |
| `C-a d` | Detach session |
| `C-a s` | List sessions |
| `C-a c` | New window (current path) |
| `C-a n/p` | Next / previous window |
| `C-a \|` | Split horizontal |
| `C-a -` | Split vertical |
| `C-a h/j/k/l` | Navigate panes |
| `C-a H/J/K/L` | Resize panes |
| `C-a Enter` | Enter copy mode |
| `v` / `y` (copy mode) | Begin selection / Copy and exit |
| `C-a r` | Reload config |
| `C-a /` | Show cheatsheet popup |

### Shell Aliases

| Alias | Command |
|-------|---------|
| `t [name]` | Attach or create session (default: `main`) |
| `ta <name>` | `tmux attach -t` |
| `ts <name>` | `tmux new-session -s` |
| `tl` | `tmux list-sessions` |
| `tk <name>` | `tmux kill-session -t` |
| `td` | `tmux detach` |

### Workspace Layouts

**dev** (default — 5 panes):
```
┌──────────────┬──────────┐
│              │  MONITOR │
│   CLAUDE     ├──────────┤
│              │  FILES   │
├──────────────┼──────────┤
│  LAZYGIT     │   SHELL  │
└──────────────┴──────────┘
```

**claude** (7 panes):
```
┌──────────────┬──────────┐
│              │  MONITOR │
│   CLAUDE     ├──────────┤
│              │  FILES   │
│              ├──────────┤
│              │  REMOTE  │
├──────────────┼─────┬────┤
│   LAZYGIT    │SHELL│LOG │
└──────────────┴─────┴────┘
```

**monitor** (4 panes):
```
┌──────────────┬──────────┐
│   MONITOR    │  SHELL   │
├──────────────┼──────────┤
│   LAZYGIT    │  LOGS    │
└──────────────┴──────────┘
```

**ahub** (agent-hub first, opt-in with `dot open <project> --layout ahub` or `dot register --layout ahub`):
```
┌──────────┬──────────────────┐
│          │  PEER A          │
│  AHUB    ├──────────────────┤
│  ≥80 col │  PEER B          │
└──────────┴──────────────────┘   + window "verify" (project shell)
```

The session takes the terminal's size. Below 200x56 it becomes **ahub-compact**: one full-size window each for `hub`, `peer-a`, `peer-b` and `verify`. AHUB opens `ahub console` when the project's hub runs (`--panels` at 80x24 or more, the plain console below), otherwise it shows why and starts `ahub up --no-agents` only when you press Enter. The peer slots take the first two of `start.agents` in the project's `.agenthub/config.local.json` (then codex, claude) and start a peer only on Enter. A running session is never rebuilt: `--layout` applies to a new session only.

`dot hub <project> [console|status|ui|git|files]` opens the project's hub: in this terminal outside tmux, or inside its workspace by focusing AHUB, adding one `hub` window to an older session. Inside the workspace, `git` and `files` focus or add a `git` (lazygit, else `git status`) or `files` (yazi, eza, tree, else `ls`) window. It starts nothing and never relayouts. With `modules.workspace.ahub_keys: true`, `C-a A` opens a menu with these (off by default; no other binding changes). Design: `docs/specs/2026-11-07-ahub-workspace-layouts.md`.

### Themes

5 built-in themes: `default`, `dracula`, `nord`, `catppuccin`, `tokyo-night`.
Session-scoped — multiple workspaces can use different themes simultaneously.

### Tool Fallback Chains

| Pane | Primary | Fallback |
|------|---------|----------|
| MONITOR | btop | htop → top |
| GIT | lazygit | git status |
| FILES | yazi | eza → tree → ls |
| CLAUDE | claude | install message |

---

## Workspace commands

`dot open` requires an explicit subcommand, so `dot aply` cannot accidentally create a project. `dot layouts` offers `dev`, `claude`, and `monitor`; `dot layouts doctor` reports tool paths and optional-tool availability.

For dev builds (no ldflags), `dot version` falls back to Go's embedded VCS info with a `-dirty` suffix if the working tree has uncommitted changes.
