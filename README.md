# dotfiles

[![Test](https://github.com/STAIxBWLB/dotfiles/actions/workflows/test.yaml/badge.svg)](https://github.com/STAIxBWLB/dotfiles/actions/workflows/test.yaml)
[![Release](https://github.com/STAIxBWLB/dotfiles/actions/workflows/release.yaml/badge.svg)](https://github.com/STAIxBWLB/dotfiles/actions/workflows/release.yaml)

Declarative user environment management + AI-powered tmux workspace manager.
A single Go binary. macOS + Linux + GPU servers. Modular, profile-based, AI-ready.

---

## Quick Start

### Install via Homebrew (recommended on macOS / Linuxbrew)

```bash
brew tap staixbwlb/cask
brew trust staixbwlb/cask
brew install dotfiles
```

Provides the `dot` binary and the `dotfiles` back-compat symlink.

Moved from `entelecheia/tap` in October 2026. On an existing install,
`brew update` follows the old tap's migration entry; if `brew info dotfiles`
still names `entelecheia/tap`, run `brew trust staixbwlb/cask` and then
`brew reinstall staixbwlb/cask/dotfiles`.

`brew trust` is what lets Homebrew load a formula from a non-official tap. On
Homebrew 6 with `HOMEBREW_REQUIRE_TAP_TRUST` set, tapping alone is not enough;
the install stops with `Refusing to load formula staixbwlb/cask/dotfiles from
untrusted tap`. It is a no-op on older Homebrew and safe to run twice.

Taps that `dot apply` adds for you (`staixbwlb/cask`, `manaflow-ai/cmux`,
`stablyai/orca`) are trusted automatically as part of the run, so this is only
needed for the bootstrap tap that installs `dot` itself.

### Install via curl (fallback)

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash
```

Use this when Homebrew isn't available or you want the bootstrap to install it for you. The installer handles prerequisites automatically:
- **macOS**: Installs Homebrew (which includes Xcode Command Line Tools)
- **Linux**: Installs Linuxbrew for consistent package management
- Downloads the `dot` binary and configures PATH

Updating from v2.70.29 or earlier: `dot update` in those versions refuses a release binary over 16 MiB, which every current release is (#231). Run the curl installer above once; from then on `dot update` works again. A Homebrew install is unaffected.

On a Homebrew install (the resolved executable sits in the Cellar), `dot update` upgrades through the brew that owns it and never writes the Cellar itself (#235). For a verified native release with the exact trusted tap recipe, it uses the bounded prebuilt path described under [Resource admission](#resource-admission-dot-admit). A stale or changed recipe falls back to the full maintenance gate, runs `brew update` only when the tap lags the GitHub release, then runs `brew upgrade <tap>/dotfiles` and checks the new version. A pinned formula is left alone with a `brew unpin` hint; `--dry-run` prints the full-gate brew commands; `--check` only reports the latest version. A standalone `dot` can target an existing managed Homebrew install with `dot update --homebrew`.

After a Homebrew upgrade, launchd fails the next spawn of a dot LaunchAgent (`spawn failed`, `needs LWCR update`): its managed launch constraint did not follow the replaced binary. `dot update` therefore reloads (bootout, then bootstrap) every loaded `com.dotfiles.*` agent that runs the upgraded binary; an unloaded one, such as a paused sync, stays unloaded. After a manual `brew upgrade`, `dot sync status` and `dot peer status` name that state with the reload command: `dot sync pause && dot sync resume` for `com.dotfiles.sync*`, `dot peer setup` for `com.dotfiles.peer` (#233).

### Setup

```bash
dot            # welcome screen with next-step guidance
dot init       # interactive TUI — name, email, profile, modules
dot apply      # apply all enabled modules
dot usecase    # detailed workflow examples
```

### Migrate from another machine

**Option A — one-stop wizard (recommended):**

```bash
# On the existing machine — one interactive run backs up everything:
# profile state, macOS app settings, AI/Maru settings, encrypted secrets
dot backup

# On the new machine (Drive already mounted)
dot restore                           # pick the source host, restore in safe order
```

`dot backup` confirms the backup root, lets you pick domains
(profile/apps/ai/secrets), asks about age keys and AI auth tokens, and
stamps profile + AI snapshots with one shared tag. `dot restore` supports
cross-host restore (any machine that backed up into the same root),
optionally runs `dot apply` after the profile restore, and preserves every
overwritten local file in per-step pre-restore backups. Unattended:
`dot backup --yes --scope profile,ai,secrets` / `dot restore --yes --host <src>`.

**Option B — individual commands:**

```bash
# On the existing machine
dot profile backup --tag "pre-migration" --include-secrets
dot apps backup                       # also snapshot per-app settings
dot ai backup                         # portable Claude/Codex/Copilot/Qwen/pi/Antigravity/Maru/MCP settings

# On the new machine (Drive already mounted)
dot profile restore --include-secrets # restores ~/.config/dotfiles + ~/.ssh/age_key*
dot apply                             # brew formulas + casks from install list
dot apps restore                      # plists, Application Support, containers
dot ai restore                        # Claude/Codex/Copilot/Antigravity/MCP settings
```

The shared backup root lives in a single cloud folder
(`<cloud>/secrets/dotfiles-backup` by default) and holds every snapshot the
user has taken across machines. Auto-detection prefers **Dropbox**
(`~/Library/CloudStorage/Dropbox` or `~/Dropbox`) and falls back to Google
Drive — both gated on a `secrets/` marker folder; override anytime with
`dot profile root <path>`. `dot secrets backup` (no argument) and the
workspace sync target default follow the same detected cloud root. `profile list` shows
every version, and
`profile restore --version <id>` rolls back to any specific one.

**Option C — plain YAML export:**

```bash
# On the existing machine — export config
dot config export ~/workspace/secrets/dotfiles-config.yaml

# On the new machine — import and apply
dot init --from ~/workspace/secrets/dotfiles-config.yaml
dot apply
# → gh auth login (if private repos configured)
# → git clone work/vault repos
# → symlink federation, shell config, packages...
```

### Workspace

```bash
dot open myproject   # launch or resume a multi-panel tmux workspace
dot open myproject   # SSH dropped? just run it again — resumes exactly
```

When the workspace module is enabled, the rendered shell environment also
exports the shared AI scratch contract:

```bash
MARU_SCRATCHPAD="$WORK/scratchpad"
MARU_TEMP="$MARU_SCRATCHPAD/temp"
CLAUDE_CODE_TMPDIR="$MARU_TEMP/runtime/claude"
```

Maru and external AI CLIs therefore resolve the same temporary-artifact root.

### Sync secret policy

`dot sync` excludes credential-bearing paths by default, including `.ssh`,
`.gnupg`, common cloud and package-manager credentials, and private-key and
keystore extensions. An operator can explicitly re-include a path in
`allow.txt`; status and dry-run previews show every such sensitive override
without changing the requested transfer decision.

### Build from source

```bash
git clone https://github.com/STAIxBWLB/dotfiles.git && cd dotfiles
make build          # → bin/dot
make install        # → ~/.local/bin/dot + ~/.local/bin/dotfiles (symlink)
```

---

For the full command reference, see [docs/commands](docs/commands/). Cross-command operating guidance, backup-root resolution, and persistent flags are in the [command guide](docs/COMMANDS-GUIDE.md). Configuration ownership is documented in [boundaries](docs/BOUNDARIES.md).

## Modules

### Execution Order

```
packages → shell → node → git → ssh → terminal → container → tmux →
workspace → ai → fonts → macapps → conda → gpg → secrets
```

### Module Details

| Module | Profile | Description |
|--------|---------|-------------|
| **packages** | minimal | Homebrew formula installation |
| **shell** | minimal | zsh, Oh My Zsh, plugins, config files |
| **node** | full | pnpm store relocation outside cloud-synced workspace trees (~/.config/pnpm/npmrc) |
| **git** | minimal | git config, aliases, global ignore |
| **ssh** | minimal | SSH config, config.d includes |
| **terminal** | minimal | starship prompt, Orca auto-install (macOS/Arch), Warp theme |
| **container** | opt-in | One `container` command with Apple syntax: apple/container on macOS, docker or podman on Linux (see [Container](#container)) |
| **tmux** | full | tmux.conf (256color, vim keys, C-a prefix) |
| **workspace** | full | Dual-workspace: git repo clone, gh auth, symlink federation (cloud mirror, vault, inbox). Vault location is selectable at init and auto-detected from existing `<workspace>/work/vault` or `<workspace>/vault`; the separate vault repo entry is skipped when the vault lives inside work (e.g. as a submodule). Cloud mirror is selected at init from detected mounts (Dropbox preferred, Google Drive accounts are listed); shell exports `CLOUD_WORKSPACE`/`CLOUD_WORK`, alias `cwork`, and the `ws()` jumper (formerly `GDRIVE_*`/`gwork`) |
| **ai** | full | AI CLI/config helpers, Claude/Codex/Copilot/Kiro/Kimi/Qwen/pi/Antigravity/Aider/Maru settings backup, optional HUD |
| **fonts** | full | Nerd Font download from GitHub Releases |
| **macapps** | full (darwin) | Install selected Homebrew casks from the embedded catalog |
| **conda** | full | Conda/Mamba `.condarc` defaults; shell hooks live in managed shell init |
| **gpg** | full | GPG agent + git commit signing |
| **secrets** | full | Age-encrypted SSH keys and shell secrets |

### Selected Agent Environments

`dot ai setup` selects Claude, Codex, Kimi, Qwen, Grok, OpenCode, Genspark
Gencode, pi and Antigravity CLIs, optional development add-ons, and shared
skills. Saved selections control installation, instruction rendering and
subsequent maintenance. Deselecting stops future managed writes without
uninstalling software or deleting data. Gencode and pi install through the
npm provider; Antigravity installs its `agy` binary into `~/.local/bin` and
self-updates in the background, so `dot ai update` defers to that
self-updater instead of reinstalling.

```bash
dot ai setup
dot ai setup --agents claude,codex --tools ripwire,ocr --skills meeting-notes --non-interactive --yes --dry-run
dot ai tools list --json
dot ai tools status --json
dot ai tools apply
dot ai update --check --json
```

The add-on allowlist is **ripwire, Open Code Review (`ocr`), GSD, claude-mem
and ponytail**. Native, portable, partial, unsupported and pending-trust states
must be read separately from installation status. Missing authentication or
trust remains a user action; setup does not copy credentials or approve hooks.
With saved selections, claude-mem prepares only the selected supported memory
adapters and transcript watches; it does not install peer bridge services or
expand unsupported Grok/OpenCode capture. Curated handoffs below are separate
from this existing raw-transcript integration.

Portable desired state lives under `modules.ai.tooling`: `agents`, `tools`,
`skills`, optional version `pins`, and `updates.enabled`. Machine-specific
paths and installation provenance are resolved locally and recorded in private
`~/.local/share/dotfiles/ai/tooling-state.json` receipts. Unknown installation
providers defer replacement. Adopted official Homebrew formulae/casks retain
their provider and update only the selected package; unavailable version pins,
third-party source adoption and foreign-home mutation require explicit action.
Status explains prerequisites and deferrals. Noninteractive setup
needs explicit or saved selections; first unattended setup requires `--agents`
(`--agents=` selects none). An empty selection never means all tools.
With explicit `--home`, provide `--skills` explicitly because interactive
registry discovery is disabled for alternate homes.

Global instructions come from the existing AGENTS SSOT and overlays. Codex
reconciles the standard home and active `CODEX_HOME` profile without duplicate
physical writes. Kimi uses `KIMI_CODE_HOME`, and OpenCode uses
`OPENCODE_CONFIG_DIR` or its XDG configuration root. An explicit `--home`
isolates writes from ambient profile overrides. Qwen adds AGENTS discovery
while preserving existing context filenames and unrelated settings.

Shared skills remain **Maru-owned**. Dot checks `maru skills capabilities
--json`, reads `maru skills list --json`, then delegates selected deployment
through `maru skills sync --check|--apply --tools <csv> --skills <csv> --json`.
The list includes flat skill records with an `installable` flag and optional
`reason`; setup offers only installable Maru-owned entries. Selected deployment
is additive: no implicit retarget, foreign-skill replacement or unselected
removal. Missing capabilities defer sharing. Generic skill source updates and
unrelated plugins are not part of dot's automatic updater.

### Adaptive Work Policies

`modules.ai.policy` describes validated operating choices separately from agent
installation. It is opt-in: inspection produces an unvalidated seed, never an
inferred subscription entitlement or a claim that an installed model is optimal.
The six workloads are `routine`, `implementation`, `deep-analysis`,
`independent-review`, `documents-teaching`, and `visual-production`.

```bash
dot ai policy inspect --json
dot --config /path/to/policy.yaml ai policy resolve --task 'Review this change' --project . --json
dot --config /path/to/policy.yaml ai policy diff --json
dot --config /path/to/policy.yaml ai policy apply --persist
dot ai policy status --json
dot ai session start --task 'Implement the approved issue' --project .
dot ai session start --input /path/to/trusted-turns.jsonl --project .
dot ai policy rollback --dry-run
```

See [the policy configuration example](docs/examples/ai-policy.yaml) and
[the implementation/evidence contract](docs/specs/2026-09-27-adaptive-ai-policy.md).
`--config` is authoritative, including omission of a policy block; saved state
cannot silently restore it. `--persist` saves the selected policy only after
usable overlays are applied. Enabled policies also participate in `dot
apply/check/diff`. User-state schema version 3 prevents older binaries from
silently rewriting away the policy.

Automatic review is distinct from YOLO bypass. Managed launches currently
support verified Claude 2.1.283 and Codex 0.157.1 adapters. Other agents are
inspected and report unsupported or unavailable review capabilities. A version
change requires revalidation, never fallback to unconditional approval.
Subscription targets require native subscription authentication and reject
detected API/provider overrides. DGX launch remains deferred until an adapter
verifies its endpoint and authentication binding; a billing label alone cannot
enable it. Neither token counts nor a configured model prove account entitlement.

Apply writes only owned launch overlays, preserving native base settings,
MCP identities, hooks and credentials. Claude consumes an additional settings
file; Codex consumes a separate native profile. The active `CODEX_HOME` is
honored, and explicit `--home` isolates inspection/overlays; foreign-home native
launch is refused. Existing inline Codex profiles are reported as legacy, not
rewritten. `rollback` removes unchanged owned overlays and leaves the desired
policy definition intact; it neither alters a running session nor disables
explicit policy resolution. Unmanaged native and Orca sessions retain their
own configuration and are not silently taken over.

Within managed launches, registered claude-mem and Obsidian lookup/record tools
have explicit no-confirmation grants. These include note creation, patches,
frontmatter/tags and memory recording, while adding no deletion/move permissions.
Vault writes remain MCP-only. The same typed grants travel through resolution,
native profiles, session changes and Maru invocation mapping. Unrelated tool
approval rules and native MCP identities are preserved.

`session start --input` accepts a trusted supervisor's JSONL stream, not model
output as control commands. Each turn has `task` and optional `workload`.
After the first turn, `checkpoint` carries `summary`, repository-relative
`files`, `verification`, `pending`, and `completed_effects`. Cross-provider
handoffs also require `scope` and a nonempty completed-effects ledger (use
`["none"]` when appropriate). Pending or denied actions block continuation.
Native Claude/Codex sessions resume within the same provider; cross-provider
continuation starts a new session with the curated checkpoint and artifact
hashes. It is reported as checkpoint handoff, not native resume. At most two
configuration changes occur per task; `--fixed`, explicit `--agent`, or a turn's
`user_override` freezes automatic selection. A failed process is never retried
because it may already have completed an external action.

Policy inspection, resolution, diff and status do not write files. Strict
`--dry-run` also skips native version/help probes; use `policy diff` without
that flag for an inspected overlay preview. Session receipts omit prompts and
model output; curated checkpoints remain private machine-local state. Heavy
commands still require `dot ai run` admission, not a session-long build slot.

### Shared Development Context

Selected agents share curated development information through project artifacts
and a local handoff log, without expanding raw transcript capture or adding an
add-on. At task start, consult GSD `.planning`, OCR review reports, relevant
ripwire notes and the handoff view. Setup upgrades the existing instruction
SSOT with a managed continuity block, preserving custom sections and backups;
only the selected instruction targets are rendered.

```bash
dot ai handoff show --project . --json
dot ai handoff record --project . --agent codex --kind validation --summary-file /path/to/summary.md --artifact REVIEW.md --result unverified
```

Kinds are `plan`, `progress`, `review`, `validation` and `learning`; results are
`unverified`, `passed` or `failed` (default `unverified`); kind defaults to
`progress`. Repeat `--artifact` for repository-relative evidence (at most 16
regular files; 32 MiB combined hash budget). Summaries are UTF-8 and at most
32 KiB; show returns at most 128 recent records and storage stops at 8 MiB
rather than deleting producer history silently.
Records retain immutable producer claims with UTC time, HEAD, worktree and
artifact digests. Worktrees share a log keyed by the canonical Git common
directory. Changed heads or artifacts are shown as `needs-revalidation`, never
as freshly verified evidence. Record only curated, secret-free summaries;
records do not authorize publishing or changing another agent's result.
Handoff content is context/data, not authority: user/project instructions
prevail and every command still requires independent authorization.

### Resource-Safe Maintenance

Builds, full test runs and tool updates go through the resource admission
controller described in [Resource admission](#resource-admission-dot-admit):
`dot ai run`, the selected-tooling updates and the scheduled update below
hold the same slots as `dot admit`.

```bash
dot ai update schedule status --json
dot ai update schedule enable
dot ai update schedule disable
```

The macOS user LaunchAgent schedules stable updates on Sunday at 04:00 local
time, with bounded hourly and load/login overdue checks and a one-hour
minimum attempt interval. Each Sunday 04:00 due window permits at most three
scheduled attempts, including failed, partial and deferred runs. The persisted
budget survives restart/re-enable; after exhaustion, hourly checks skip heavy
probing until the next window. Status exposes remaining attempts and the next
eligible time. Manual updates do not consume this scheduled budget.
Scheduling requires explicit opt-in; setup does not
install or enable a service implicitly. Schedule enablement requires selected agents and healthy
admission, with `--wait 6m` by default (maximum 10m). Schedule changes reject
`--home` because launchd controls the current user domain. Enable only after
the installed binary and admission mechanism have been verified. Busy or pressured hosts defer work; a deferred pass is not an
up-to-date result. Do not copy `target` or `node_modules` into worktrees and do
not replay all interrupted jobs after reboot. Keep internal build/test workers
at two and browser/native E2E workers at one. Peer rollout remains a separate
per-machine verification step when the peer is reachable.

### Prompt Styles

The terminal module deploys a Starship prompt config. Two styles are selectable
during `dot init` or `dot reconfigure`:

| Style | Default for | Character | Info shown |
|-------|-------------|-----------|------------|
| **minimal** | minimal, server | `>` | truncated path, branch, dirty marker |
| **rich** | full | `→` | time, user, path, host, branch+status, language versions, duration |

```bash
dot apply --module terminal     # deploys the selected style
dot reconfigure                 # switch between minimal ↔ rich
```

Config key: `modules.terminal.prompt_style` (state: `modules.prompt_style`).

### Terminal Apps

`dot init` and `dot reconfigure` include a non-server terminal app selection.
The fresh `full` profile defaults to Orca. macOS offers `orca`, `warp`, `wave`,
`cmux`, and `iterm2`; Arch Linux offers Orca. Existing explicit selections are
preserved.

Selections are stored in `modules.terminal_apps.apps`. The legacy `casks` key
is accepted on load and rewritten as `apps` on the next save. Selecting `warp`
also enables the managed Warp theme file.

On macOS, regular `dot apply` installs a missing Orca through Homebrew:

```bash
brew install --cask stablyai/orca/orca
```

On Arch Linux, the terminal module accepts either `stably-orca-bin` or
`stably-orca-git` as installed and otherwise runs:

```bash
yay -S --needed stably-orca-bin
```

`yay` is a prerequisite; dot reports the required command and stops if the AUR
helper is unavailable. Other Linux distributions do not attempt an automatic
GUI app install. This preference controls dot's selected terminal workspace
app and does not register an operating-system terminal command handler.

### Container

The `container` module is opt-in in every profile. `dot container setup`
enables it in user state, installs the backend, and writes the shim
`~/.local/share/dotfiles/shims/container`, which `00-exports.sh` puts ahead of
the brew bin dirs.

The opt-in syncs with `~/.config/dotfiles`, but each host chooses whether to
install: `dot container setup` is that host's yes, and `dot apply` run from a
terminal asks a host that has not chosen (a no is remembered in the host's
`state.json`). `dot apply --yes` and scheduled runs never install on an
undecided host. Every host that is set up takes Apple `container` syntax:

| Host | Backend | Setup installs |
|------|---------|----------------|
| macOS 26+ on Apple silicon | apple/container | `brew install container`, `brew services start container`, the recommended guest kernel |
| Linux with a usable docker | docker | nothing; an existing docker is never changed |
| Linux without docker or podman | docker, or podman when declined | the distro package (`docker.io` or `docker`), `systemctl enable --now docker`, the `docker` group (root-equivalent; log in again) |

Intel Macs and macOS before 26 are reported as unsupported and skipped. On
Linux the shim translates verbs (`list` → `ps`, `image list` → `image ls`,
`registry login` → `login`, `system status` → `info`) and flags (`-c` →
`--cpus`, `-a`/`--os` → `--platform`, `--format yaml` is refused). Verbs with
no Linux meaning (`system start`, `builder start`) are no-ops; `system
kernel`, `machine` and `k8s` exit 2. `DOT_CONTAINER_TRACE=1` prints the argv
the shim runs.

`modules.container.dns` adds `--dns` to `run`, `create`, `build` and
`builder start` (on Linux `run` and `create` only, since docker `build` has
no `--dns`) unless the call already passes `--dns` or `--no-dns`. When
Cloudflare WARP holds port 53, DNS inside apple/container fails
(apple/container#402) and setup offers `[1.1.1.1, 1.0.0.1]`. `dot container
status [--probe]` reports the backend, service, shim, DNS and WARP state.

### Packages

**minimal** (17):
`git`, `git-lfs`, `gh`, `age`, `rsync`, `fzf`, `ripgrep`, `fd`, `bat`, `jq`, `yq`, `direnv`, `zoxide`, `eza`, `starship`, `curl`, `fnm`

**full** adds (+11 unique):
`maru-cli`, `btop`, `lazygit`, `yazi`, `glow`, `csvlens`, `chafa`, `uv`, `pipx`, `tmux`, `gnupg`

**server** adds (+4):
`btop`, `tmux`, `uv`, `pipx`

---

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

## Profiles

Profiles use YAML inheritance. `full` extends `minimal`.

| Profile | Modules | Packages | Use Case |
|---------|---------|----------|----------|
| **minimal** | 5 | 17 | Lightweight dev setup |
| **full** | 14 | 28 | Complete workstation (macapps enabled on darwin) |
| **server** | 8 | 21 | GPU/DGX server |

**server**: Extends `minimal` + tmux, ai, conda. Disables workspace, fonts, macapps, gpg, secrets. Auto-suggested when NVIDIA GPU or CUDA is detected.

---

## Configuration

User settings are stored in `~/.config/dotfiles/config.yaml`:

```yaml
name: "Young Joon Lee"
email: "hello@jeju.ai"
github_user: "entelecheia"
timezone: "Asia/Seoul"
profile: "full"
modules:
  workspace:
    path: "~/workspace"
    # vault: "~/workspace/work/vault"  # optional; auto-detected when omitted (default <path>/work/vault)
    repos:
      - name: work
        remote: "git@github.com:user/work.git"
      # vault repo entry only when the vault is a STANDALONE repo at <path>/vault;
      # skipped automatically when the vault lives inside work (e.g. a submodule)
      - name: vault
        remote: "git@github.com:user/vault.git"
  ai:
    enabled: true
  prompt_style: rich    # "minimal" or "rich"
  terminal_apps:
    enabled: true
    apps:
      - orca
  fonts:
    family: "FiraCode"
  macapps:
    enabled: true
    casks:          # install list (catalog tokens)
      - 1password
      - raycast
      - obsidian
    casks_extra:    # install list (free-form additions)
      - maccy
    backup_apps:    # backup/restore scope; empty = manifest ∩ installed
      - raycast
      - obsidian
    backup_root: "~/Library/CloudStorage/GoogleDrive-*/My Drive/secrets/dotfiles-backup"
  container:
    enabled: true
    backend: auto          # auto | apple | docker | podman
    dns: [1.1.1.1, 1.0.0.1]  # default --dns; empty = backend default
  rsync:
    remote_host: "user@ubuntu-server"
    remote_path: "~/workspace/work/"
    interval: 300
ssh:
  key_name: "id_ed25519_entelecheia"
secrets:
  age_identity: "~/.ssh/age_key_entelecheia"
  age_recipients:
    - "age1..."
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `DOTFILES_YES` | Set to `true` for unattended mode |
| `DOTFILES_PROFILE` | Override profile name |
| `DOTFILES_NAME` | Override user name |
| `DOTFILES_EMAIL` | Override email |
| `DOTFILES_WORKSPACE_PATH` | Override workspace path |
| `DOTFILES_REPO_DIR` | Dotfiles repo directory |
| `DOTFILES_HOME` | Override home directory |
| `GITHUB_TOKEN` | GitHub API token for `update` |
| `DOT_CONTAINER_TRACE` | Set to `1` to print the backend argv the `container` shim runs |
| `DOT_SCHEMA_FORCE` | Set to `1` to overwrite a state file written by a newer `dot`, dropping any keys this binary does not know |

### Peer profile

`dot peer` keeps its settings in `<workspace>/.dotfiles/peer/config.yaml`, one per machine (the store is gitignored). Keys besides those `dot peer init` writes:

Tracked home paths in `home-paths-tracked.txt` include regular files and symlinks,
including dangling links and links to directories. Links are compared by the
SHA-256 of their target string and are never followed. Link deletion and
retargeting use the same baseline, tombstones, conflict quarantine and
`max_delete` limit as regular files. Workspace payloads remain regular-file-only.
Populated-directory versus link replacements stop during planning; resolve the
node-type conflict explicitly before retrying. Pathnames containing control
characters retain the existing inventory restrictions. Ancestor preflight and
destination revalidation catch changes observed before transfer; the existing
path-based rsync protocol does not make checks atomic with a different process
replacing directories during the operation (see `docs/CEILINGS.md`).
Upgrade both peers before relying on link deletion propagation. Existing home
baselines contain no link history: the first complete sync records the current
links using the normal first-seen reconciliation rules; it cannot infer links
deleted before that baseline was recorded. Delete an unwanted link after this
first sync to propagate its removal. If both peers hold different previously
untracked targets, normal conflict quarantine preserves the losing copy.

| Key | Description |
|-----|-------------|
| `host_merge` | Host files (relative to `$HOME`) whose listed top-level JSON keys are merged from both Macs before the additive host pass of a two-way `dot peer sync`, e.g. `host_merge: {.claude.json: [mcpServers, projects]}`: every entry either copy has survives, the newer copy wins an entry both have and every other key. Without it the newer file wins whole; `dot peer diff --list` marks such hot files and warns about entries newest-wins would drop. The merge is a union: an entry removed on one Mac comes back from the other, so remove it on both. It applies to files the additive host list (`home-paths.txt`) covers, not tracked ones or files under a tool skill root. A listed file never goes through newest-wins: a regular file on a single Mac is created on the other where it is absent (rsync `--ignore-existing`), one on both is merged. A copy it cannot merge (a symlink, a directory, not a JSON object) stops a two-way run before anything moves, and `peer diff --list` shows why. A `--push-only` or `--pull-only` run merges nothing: it holds a listed file present on both Macs and says so. The config is per machine and only the coordinator's applies: set it on both Macs so it survives a handover, and upgrade dot on both first, since an older dot drops the key when it saves. |
| `owner_aliases` | Earlier names of the owner, written by `dot sync owner --rename <old> <new>` (in every profile, the mirror's too) so a Mac still answering to the old name keeps its role during a rename. Only the Mac being renamed records them; the coordinator retires them at the first complete peer sync where the peer records the new owner and this Mac answers to it; where the coordinator's peer run does not reach (a mirror-only workspace, or the mirror owner when another Mac coordinates the peer) they stay until the owner next changes (docs/CEILINGS.md). `--set`, `--set-self` and `--clear` always drop them; any other change to a different owner drops them. Not edited by hand. |
| `hooks` | Actions around this Mac's coordinator role, so the inactive Mac runs no jobs that write the workspace. `on_activate` runs after `dot peer setup` installs the scheduler (also the step a handover runs on the new coordinator, and the one a takeover names next); `on_deactivate` after `dot peer setup --off`, on the old coordinator in a handover, in a fence demotion before dot's own scheduler goes, and after a peer `dot sync owner --set/--clear` that takes the role from this Mac. Actions: `launchd-bootout <label-glob>` disables the jobs of `~/Library/LaunchAgents/<glob>.plist` (so a reboot does not load them again) and boots out the loaded ones, recording which it disabled; `launchd-bootstrap <label-glob>` re-enables only those and bootstraps the ones not loaded, so a job stopped outside dot (Maru's Stop) stays stopped. The glob starts with a literal prefix and never matches `com.dotfiles.peer`. Maru labels end in a hash of the workspace path (`com.maru.job.<id>.<hash>`), so `com.maru.job.*.<hash>` leaves other workspaces' jobs alone; a glob that matches no plist fails. `app-quit <App>` sends an Apple Event, which macOS allows per sending program: in a scheduled demotion that is dot itself, to be allowed under Privacy & Security > Automation when it first asks; until then the action fails and the app keeps running. `app-open <App>`. Each action has a one-minute limit. List `app-quit` of the app your terminal runs in last: quitting it ends the command. Upgrade dot on both Macs before adding hooks: a dot without hooks runs none, and one from before #196 also drops the key when it saves (later releases keep top-level keys they do not know, not a new sub-key of a known one; a handover to an older peer says so first). `--dry-run` lists them; results go to the output and the peer log; a failure never stops the command. Example: `on_deactivate: [launchd-bootout com.maru.job.*, app-quit Maru]`. |
| `remote_dot` | Path of the `dot` binary to run on the other Mac (`~/` is that Mac's home). Unset, a peer run probes `~/.local/bin/dot`, `/opt/homebrew/bin/dot`, `/usr/local/bin/dot`, the Linuxbrew path and `command -v dot` there, and uses the newest release; a dev build is used only when nothing else is installed, with a warning. Set it to use a dev build; scheduled runs read it too, and so does `dot ai memory sync` to that host (its other peers get the newest release). |

---

## Resource admission (`dot admit`)

One controller admits heavy work (builds, full test runs, indexing, bulk copies, dependency and tool updates): at most one heavy job per project repo at a time, shared across all its worktrees, branches, agent sessions and runtime profiles, while different repos run in parallel. Global tool installs and updates share one host-wide maintenance slot instead. Two entry points use the same slots, pressure history and state:

- `dot admit -- COMMAND` exits 75 at once on host pressure and waits up to `--wait` (default 30 minutes; `--wait 0` fails fast) for a busy slot; `--class maintenance` takes the maintenance slot.
- `dot ai run [--project <checkout>] -- COMMAND` waits up to `--wait` (default 6 minutes, at most 10) and runs the command with `CARGO_BUILD_JOBS=2`, `RUST_TEST_THREADS=2` and `GOMAXPROCS=2`. The selected-tooling updates (`dot ai update`, `dot ai tools apply` and the AI module of `dot apply`) take the maintenance slot and defer at once when it is busy; the scheduled update pass waits up to 6 minutes for it, and `dot ai update schedule enable` up to its `--wait` (default 6 minutes, at most 10).

A host-pressure gate defers new work on memory pressure warning/critical, a serious or critical macOS thermal state, CPU idle below 15% or load1 at the CPU count sustained 60s, or a recent WindowServer watchdog termination; after such an episode, recovery requires five continuous minutes of normal telemetry. Both the 60-second CPU excursion and 5-minute recovery require completed samples no more than 45 seconds apart. Missing/stale samples, timestamp rollback, or a larger gap reset the streak, and an active defer stays active until a fresh sequence completes. Missing or failed telemetry always defers: the gate never reports a machine it cannot measure as healthy. Once a slot is held, both entry points also scan the process table for heavy work that holds no slot (builds, tests, installs, bulk copies, or anything at 100% CPU) and defer when it runs in the same repository or its repository is unknown (for the maintenance slot: install, update, upgrade, sync or CI work in any repository). Work inside a live slot holder's process tree is not counted. The scan is a bounded heuristic, not proof that every process on the machine is controlled; the controller never kills other sessions or restarts system services.

`dot update` has a narrowly scoped exception for a verified native release prebuilt. It still takes the host-wide maintenance slot. Memory warning is tolerated only after 30 seconds of fresh healthy samples with no swap-in or swap-out; samples more than 45 seconds apart, missing telemetry, critical memory, serious/critical thermal state, recent WindowServer watchdog, idle below 30%, load1 at or above 70% of CPU count, or any swap activity defer the update. This policy has its own history and does not modify heavy-work admission history. The release must be the exact stable GitHub asset for the native platform, match `checksums.txt`, contain one root `dot` executable and at most the bounded root `LICENSE` and `README.md`, and report the requested version. Limits are 32 MiB compressed archive, 64 MiB binary, 1 MiB per release document, 67 MiB for the entire expanded gzip stream including tar metadata and padding, and 1 MiB for `checksums.txt`. For Homebrew, the installed binary and keg version, exact raw recipe under the trusted tap, native URL, checksum, target version, and unpinned state must match. The warning-tolerant path also requires Homebrew's existing portable Ruby and Bundler to be ready: the version marker must match the `portable-ruby/current` link and both binaries must be executable. Both default and XDG Homebrew `brew.env` locations are scanned; any active `HOMEBREW_` assignment, Ruby-mode or XDG process override, or relative Homebrew config path sends the operation through the full maintenance gate. Before and after admission, dot checks the recipe, pin, installed version, runtime readiness, the running updater's native architecture, and the current Homebrew opt binary's native architecture. After `brew upgrade`, it verifies both the installed version and native architecture. The canonical recipe has no dependencies or post-install hook. Any unverified recipe or stale metadata uses the full maintenance gate. A standalone binary can target an existing Homebrew install with `dot update --homebrew`; Homebrew performs the managed update and dot does not write the Cellar directly. This exception does not cover generic refreshes, installs, or other tool updates.

```bash
dot admit -- make test                                 # one heavy slot per repo
dot admit --class maintenance -- brew upgrade          # the single host-wide maintenance slot
CARGO_BUILD_JOBS=2 dot ai run --project . --wait 6m -- cargo test -- --test-threads=2
dot admit status                                       # owners, pressure evidence, hysteresis countdown
```

On defer `dot admit` exits 75 (`EX_TEMPFAIL`) and `--json` prints the machine-readable outcome (`scope`, `owner`, `reason`, `retry_after`). Leases carry pid + start time and a heartbeat, so a crashed owner's slot is reclaimed safely, and a job launched inside an admitted job runs in its parent's slot when it asks for the same scope and class. Thresholds mirror the workspace resource policy. State lives in the real user's `~/.local/state/dot/admission/`; `--home` does not partition it.

---

## Architecture

Same modular Go architecture as [rootfiles](https://github.com/STAIxBWLB/rootfiles).

Interactive blueprint: [docs/architecture/dotfiles-v2-rendered.html](docs/architecture/dotfiles-v2-rendered.html) (spec: [dotfiles-v2.architecture.json](docs/architecture/dotfiles-v2.architecture.json)).

```
rootfiles (root, server)        dotfiles (user, workstation)
━━━━━━━━━━━━━━━━━━━━━━━━━━━     ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Packages (APT), users, SSH       Packages (Homebrew), shell, git
Docker, GPUs, tunnels            Terminal, fonts, AI
Locale, firewall, storage        Workspace, secrets, sync, tmux
```

### Project Structure

```
dotfiles/
├── cmd/dot/main.go               # Entry point (ldflags: version, commit)
├── internal/
│   ├── cli/                      # Cobra commands
│   │   ├── open.go               # dot open — workspace launcher
│   │   ├── sync_cmd.go           # dot sync — workspace sync (local mirror or SSH)
│   │   ├── clean_cmd.go          # dot clean — workspace junk cleanup
│   │   ├── status_cmd.go         # dot status — unified dashboard
│   │   └── workspace_cmds.go     # stop, list, register, unregister, layouts, doctor
│   ├── config/                   # Config struct, loader, detector, state
│   │   └── profiles/             # Embedded YAML profiles (go:embed)
│   ├── aisettings/               # AI assistant settings backup/restore/export/import
│   ├── clean/                    # Workspace cleanup scanner + deletion
│   ├── exec/                     # Runner (dry-run), Brew wrapper
│   ├── module/                   # 14 module implementations (macapps darwin-only)
│   ├── syncer/                   # Workspace sync engine (used by dot sync)
│   │   ├── scheduler.go          # Scheduler types
│   │   ├── scheduler_darwin.go   # macOS launchd
│   │   └── scheduler_other.go    # Linux systemd
│   ├── watchdog/                 # Runaway-process reaper + notifier (used by dot watchdog)
│   ├── admission/                # Repo-scoped heavy-job slots + host-pressure gate (used by dot admit)
│   ├── workspace/                # Workspace management
│   │   ├── config.go             # Project config, YAML load/save
│   │   ├── deploy.go             # Shell script deployer (go:embed)
│   │   └── scripts/              # Embedded shell scripts
│   ├── template/                 # Go text/template engine
│   │   └── templates/            # Embedded templates (go:embed)
│   ├── fileutil/                 # File ops, download, hash compare
│   └── ui/                       # Charm huh TUI wrapper
├── tests/                        # Integration + scenario tests
├── scripts/install.sh            # curl-pipe installer
├── .goreleaser.yaml              # Cross-platform release config
└── .github/workflows/            # CI: test → release pipeline
```

### Key Design

- **Module interface**: `Check()` → `Apply()` — idempotent, dry-run aware
- **Profile inheritance**: YAML `extends` chain with field-level merging
- **go:embed**: Profiles, templates, and scripts compiled into the binary
- **SHA256 hash**: Skip writes when content unchanged, backup before overwrite
- **Non-fatal errors**: Module failures logged, remaining modules continue
- **Platform build tags**: Platform-specific code (xattr, launchd, systemd) via `//go:build`

---

## CI/CD

### Test Pipeline

| Job | Matrix | Description |
|-----|--------|-------------|
| **lint** | ubuntu-latest | golangci-lint |
| **unit** | ubuntu-latest, macos-latest | Go unit tests + coverage |
| **integration** | ubuntu-24.04 × {minimal,full,server} + server image | Docker-based profile tests |
| **linux** | modules + 10 scenarios on ubuntu-22.04 image | Module and E2E scenario suite |
| **apps-install-macos** | macos-latest | macOS cask install plus macapps scenario |

**Release**: Triggered by `workflow_run` — only after Test succeeds on a `v*` tag. Uses GoReleaser for cross-platform builds (darwin/linux × amd64/arm64).

### Creating a Release

Add `docs/releases/vX.Y.Z.md` (format: `AGENTS.md`, Release notes) and merge it first; the Release workflow publishes it as the release body and stops without it.

```bash
git tag v0.9.0
git push origin v0.9.0
# Test workflow runs → on success → Release workflow creates GitHub Release
```

---

## GPU Server Provisioning

On a fresh DGX or GPU server — auto-detects NVIDIA GPU + CUDA:

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash
dot init --yes     # auto-selects 'server' profile
dot apply --yes    # packages (incl. rsync), shell, git, ssh, terminal, tmux, ai, conda
```

Or import config from your workstation:

```bash
dot init --from ~/workspace/secrets/dotfiles-config.yaml
dot apply --yes
```

Detection: `nvidia-smi` (GPU model), `/usr/local/cuda` (CUDA home), `/etc/dgx-release` (DGX).

---

## Development

Source builds require Go 1.25.8 or newer.

```bash
make build      # build binary
make test       # run tests
make lint       # lint
make clean      # clean artifacts
make install    # install to ~/.local/bin/
```

## License

MIT
