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

Opening a project takes the explicit `dot open <project>`, so a typo such as `dot aply` cannot create a project. `dot layouts` lists `dev` (the default), `claude`, `monitor`, `ahub` and `ahub-compact`; `dot layouts doctor` reports tool paths and optional-tool availability.

For dev builds (no ldflags), `dot version` falls back to Go's embedded VCS info with a `-dirty` suffix if the working tree has uncommitted changes.

## Workspace module

The `workspace` module (full profile) clones the work and vault repos, runs
`gh` authentication and links the workspace to the cloud mirror, vault and
inbox.

- **Vault:** `dot init` lets you pick the vault location, and it is
  auto-detected from an existing `<workspace>/work/vault` or
  `<workspace>/vault`. When the vault lives inside work, for example as a
  submodule, the separate vault repo entry is skipped.
- **Cloud mirror:** `dot init` picks it from the mounts it detects. Dropbox is
  preferred, and Google Drive accounts are listed.

The rendered shell init exports:

```bash
export WORKSPACE="<workspace path>"
export WORK="$WORKSPACE/work"
export VAULT="<vault path>"
export MARU_SCRATCHPAD="$WORK/scratchpad"
export MARU_TEMP="$MARU_SCRATCHPAD/temp"
export CLAUDE_CODE_TMPDIR="$MARU_TEMP/runtime/claude"
# only when a cloud mirror is set (formerly GDRIVE_*):
export CLOUD_WORKSPACE="<cloud mirror path>"
export CLOUD_WORK="$CLOUD_WORKSPACE/work"
```

The last three give Maru and external AI CLIs one shared root for temporary
artifacts.

It also defines these shortcuts:

- `work`, `vault` and `cwork` (formerly `gwork`) change to those roots.
- `ws` switches between a folder under `$WORK` and the matching folder under
  `$CLOUD_WORK`, in either direction.
