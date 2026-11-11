# Configuration

## Prompt Styles

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

## Your Own Shell and SSH Lines

`dot apply` rewrites `~/.zshrc`, `~/.bashrc` and `~/.ssh/config` whole, so
lines added to them by hand or by an installer are removed. Put them where dot
never writes:

| File | Read by |
|------|---------|
| `~/.zshrc.local` | last line of `~/.zshrc` |
| `~/.bashrc.local` | last line of `~/.bashrc` |
| `~/.ssh/config.d/*` | first line of `~/.ssh/config`, so it applies to every host and its values win over the defaults below it |

`dot diff` lists the files `dot apply` would rewrite.

## Container Service

On a host where the container backend is already installed, `dot apply` writes
the shim and its state file but leaves the service as you left it: it does not
start a stopped container system or re-register its login job. Run
`dot container setup` to start it.

## Terminal Apps

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

## Packages

**minimal** (17):
`git`, `git-lfs`, `gh`, `age`, `rsync`, `fzf`, `ripgrep`, `fd`, `bat`, `jq`, `yq`, `direnv`, `zoxide`, `eza`, `starship`, `curl`, `fnm`

**full** adds (+11 unique):
`maru-cli`, `btop`, `lazygit`, `yazi`, `glow`, `csvlens`, `chafa`, `uv`, `pipx`, `tmux`, `gnupg`

plus the `maru-workspace` cask.

**server** adds (+4):
`btop`, `tmux`, `uv`, `pipx`

---

## Configuration

User settings are stored in `~/.config/dotfiles/config.yaml`:

```yaml
name: "Your Name"
email: "you@example.com"
github_user: "your-github-user"
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
  conda:
    enabled: false         # turn the profile's conda module off; omit to keep the profile default
  rsync:
    remote_host: "user@ubuntu-server"
    remote_path: "~/workspace/work/"
    interval: 300
ssh:
  key_name: "id_ed25519"
secrets:
  age_identity: "~/.ssh/age_key"
  age_recipients:
    - "age1..."
admission:              # optional resource-admission overrides (dot admit)
  thresholds:           # host-pressure gate
    idle_defer_below: 15        # percent; the values shown are the defaults
    idle_recover_at: 30
    load_defer_frac: 1.0        # load1 at this multiple of the CPU count
    load_recover_frac: 0.7
    defer_sustain: 60s
    recover_sustain: 5m
    ws_grace: 30m
    mem_avail_warn_pct: 10      # Linux MemAvailable percent
    mem_avail_critical_pct: 5
  prebuilt:             # verified prebuilt dot update
    idle_defer_below: 30
    idle_recover_at: 30
    load_defer_frac: 0.7
    load_recover_frac: 0.7
    safety_window: 30s          # quiet telemetry required before admitting
  slots:                # concurrent leases per scope, 1-8
    heavy_per_repo: 1
    maintenance: 1
  uncovered:            # heavy work that holds no slot
    enabled: true
    cpu_percent: 100            # CPU-only rule; 0 turns it off
    ignore_names: []            # exact process basenames never counted
```

Every `admission` key is optional and independent; an unset key keeps the policy default. A duration written as a plain number is a count of seconds, and one too large to fit a duration is refused instead of wrapping. A duration of `0s` is a real value (`defer_sustain: 0` defers at the first over-threshold sample), distinct from leaving the key out, and it survives a state save. Values are validated when the policy resolves: percentages must stay within 0-100 and be finite, load fractions must be above zero, durations must be at or above zero, the recovery side must be no stricter than the defer side (`idle_recover_at >= idle_defer_below`, `load_recover_frac <= load_defer_frac`, `mem_avail_critical_pct <= mem_avail_warn_pct`), slot counts must be between 1 and 8, and `uncovered.cpu_percent` at or above zero. An invalid value fails `dot admit` and `dot admit relax` with a message naming the key and the config file; `dot admit status` reports the same error and still renders its sections against the defaults, because a blocked config is exactly when that view is wanted. For a temporary change use `dot admit relax --set <key>=<value>` or `--ignore <rules>` (see [Resource admission](maintenance.md#resource-admission-dot-admit)) rather than editing this file.

### Environment Variables

| Variable | Description |
|----------|-------------|
| `DOTFILES_YES` | Set to `true` for unattended mode |
| `DOTFILES_PROFILE` | Override profile name |
| `DOTFILES_NAME` | Override user name |
| `DOTFILES_EMAIL` | Override email |
| `DOTFILES_WORKSPACE_PATH` | Override workspace path |
| `DOTFILES_HOME` | Override home directory |
| `GITHUB_TOKEN` | GitHub API token for `dot update`; avoids the unauthenticated API rate limit (HTTP 403) |
| `DOT_CONTAINER_TRACE` | Set to `1` to print the backend argv the `container` shim runs |
| `DOT_ADMISSION_IGNORE` | Rules to step past for one admitted run: `pressure`, `slot`, `uncovered`, or `all` (comma-separated); honored by every wrapped caller |
| `DOT_SCHEMA_FORCE` | Set to `1` to overwrite a state file written by a newer `dot`, dropping any keys this binary does not know |

## Global flags

All commands inherit `--config`, `--dry-run`, `--home` and `--yes`. `--profile` (minimal, full, server) is taken by the commands that load a profile: `apply`, `diff`, `config`, `status`, `init` (which preselects it), `watchdog setup` and `notify`, and `container setup`. `--module` is taken by `apply` and `diff`. `dot sync` names its store with `--store`. Generated pages list each command's own options.
