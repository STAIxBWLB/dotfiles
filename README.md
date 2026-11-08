# dotfiles

[![Test](https://github.com/STAIxBWLB/dotfiles/actions/workflows/test.yaml/badge.svg)](https://github.com/STAIxBWLB/dotfiles/actions/workflows/test.yaml)
[![Release](https://github.com/STAIxBWLB/dotfiles/actions/workflows/release.yaml/badge.svg)](https://github.com/STAIxBWLB/dotfiles/actions/workflows/release.yaml)

Declarative user environment management + AI-powered tmux workspace manager.
A single Go binary. macOS + Linux + GPU servers. Modular, profile-based, AI-ready.

---

## Quick Start

### Install via Homebrew (recommended on macOS / Linuxbrew)

```bash
brew install staixbwlb/cask/dotfiles
```

Installs the `dotfiles` cask: the `dot` binary and the `dotfiles` back-compat
link, on macOS and on Linuxbrew. An install from the earlier `dotfiles`
formula moves to the cask on the next `brew update` or `dot update` (#344).

Moved from `entelecheia/tap` in October 2026. On an existing install,
`brew update` follows the old tap's migration entry; if `brew info dotfiles`
still names `entelecheia/tap`, run `brew trust staixbwlb/cask` and then
`brew reinstall staixbwlb/cask/dotfiles`.

The full name taps `staixbwlb/cask` and trusts the cask as it installs.
Homebrew 6 and later refuse a short name from an untrusted tap, so
`brew tap staixbwlb/cask && brew install dotfiles` stops with `Refusing to load
cask staixbwlb/cask/dotfiles from untrusted tap`; run
`brew trust staixbwlb/cask` first if you prefer the short name.

A new release reaches the tap a few minutes after GitHub, once the tap's audit
passes. Until then `dot update` on a Homebrew install says the tap does not
offer the new version yet; run it again shortly.

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

Standalone installs support `dot update --version 2.71.5` (or `v2.71.5`) to select an exact stable release, including a downgrade. The archive checksum, native architecture and exact runtime version are verified before activation. Binaries live in `~/.local/share/dotfiles/tools/dot/<version>/bin/dot`; `current` and `previous` are relative links, and `~/.local/bin/dot` points through `current`. The first update preserves the existing standalone binary (development builds use a content-hashed `snapshot-*` directory) and keeps its old path working through `current`. `dot rollback` activates the previous binary without downloading; another rollback switches back when that release also supports `rollback` (for an older release, run the saved newer binary at `<version>/bin/dot rollback`). Activation keeps current, previous and any version a running process uses; older idle versions are pruned with the family-tool retention rule. If a process uses an unresolved PATH/current entry or process inspection fails, versions are kept conservatively until a later activation. `--home X` writes the version tree and PATH entry only under X and leaves the invoking user's standalone executable in place. Guard hooks use a stable `current` or Homebrew `opt` path, so they follow explicit activation and rollback without rewriting settings. Homebrew owns its versions: `dot update --version` and `dot rollback` refuse there and name the brew commands to use instead.

On a Homebrew install (the resolved executable sits in the Cellar for the formula, or in the Caskroom for the cask), `dot update` upgrades through the brew that owns it and never writes the Cellar or Caskroom itself (#235, #344). For a verified native release with the exact trusted tap recipe (`Formula/dotfiles.rb` or `Casks/dotfiles.rb`), it uses the bounded prebuilt path described under [Resource admission](docs/guides/maintenance.md#resource-admission-dot-admit). A stale or changed recipe falls back to the full maintenance gate, runs `brew update` only when the tap lags the GitHub release, then runs `brew upgrade --formula <tap>/dotfiles` (`--cask` for the cask) and checks the new version. When that `brew update` moves a formula install to the cask (the tap's `tap_migrations.json`), `dot update` carries on as the cask and lists what still runs the formula's opt link (guard hooks, the noindex agent, the container shim) before you remove the old keg; when brew could not move it, for example on a Linuxbrew prefix without a `Caskroom`, it names `brew uninstall --formula --force dotfiles && brew install --cask staixbwlb/cask/dotfiles`. A pinned formula is left alone with a `brew unpin` hint; `--dry-run` prints the full-gate brew commands; `--check` reports the latest version and which path an update would take, naming the check that keeps it off the verified-prebuilt path; `--wait 30m` waits for a busy maintenance slot instead of deferring at once. A standalone `dot` can target an existing managed Homebrew install with `dot update --homebrew`.

After a Homebrew upgrade, launchd fails the next spawn of a dot LaunchAgent (`spawn failed`, `needs LWCR update`): its managed launch constraint did not follow the replaced binary. `dot update` therefore reloads (bootout, then bootstrap) every loaded `com.dotfiles.*` agent that runs the upgraded binary; an unloaded one, such as a paused sync, stays unloaded. After a manual `brew upgrade`, `dot sync status` and `dot peer status` name that state with the reload command: `dot sync pause && dot sync resume` for `com.dotfiles.sync*`, `dot peer setup` for `com.dotfiles.peer` (#233).

### Setup

```bash
dot            # welcome screen with next-step guidance
dot init       # interactive TUI — name, email, profile, modules
dot apply      # apply all enabled modules
dot usecase    # detailed workflow examples
```

### Migrate from another machine

Run `dot backup` on the old machine and, after installing dot, `dot restore` on the new one. Scopes, unattended runs and the manual sequence are in the [maintenance guide](docs/guides/maintenance.md#migrate-from-another-machine).

### Workspace

```bash
dot open myproject   # launch or resume a multi-panel tmux workspace
dot open myproject   # SSH dropped? just run it again — resumes exactly
dot open myproject --layout ahub   # agent-hub console beside two peer slots
dot hub myproject    # jump to the project's ahub console
```

When the workspace module is enabled, the rendered shell environment also
exports the shared AI scratch contract:

```bash
MARU_SCRATCHPAD="$WORK/scratchpad"
MARU_TEMP="$MARU_SCRATCHPAD/temp"
CLAUDE_CODE_TMPDIR="$MARU_TEMP/runtime/claude"
```

Maru and external AI CLIs therefore resolve the same temporary-artifact root.

### Build from source

```bash
git clone https://github.com/STAIxBWLB/dotfiles.git && cd dotfiles
make build          # → bin/dot
make install        # → ~/.local/bin/dot + ~/.local/bin/dotfiles (symlink)
```

---

For the full command reference, see [docs/commands](docs/commands/). Configuration ownership is documented in [boundaries](docs/BOUNDARIES.md).

## Guides

| Guide | Covers |
|---|---|
| [AI agents](docs/guides/agents.md) | Selected agents and add-ons, work policies, shared context, the agent-hub family, memory, guard hooks |
| [Sync and peer](docs/guides/sync.md) | `dot sync` to a mirror, secret policy, conflicts, `dot peer` between two machines |
| [Workspaces and tmux](docs/guides/workspace.md) | `dot open`, layouts, `dot hub`, key bindings, themes, `dot ws` |
| [Configuration](docs/guides/configuration.md) | The config file, environment variables, prompt styles, terminal apps, packages |
| [Maintenance](docs/guides/maintenance.md) | Backup and migration, updates, `dot admit`, resource-safe maintenance, watchdog |
| [Servers and containers](docs/guides/servers.md) | GPU server provisioning, `dot container`, Cloudflare Tunnel |
| [Architecture](docs/guides/architecture.md) | Package map and data flow |

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
| **container** | opt-in | One `container` command with Apple syntax: apple/container on macOS, docker or podman on Linux (see [Container](docs/guides/servers.md#container)) |
| **tmux** | full | tmux.conf (256color, vim keys, C-a prefix) |
| **workspace** | full | Dual-workspace: git repo clone, gh auth, symlink federation (cloud mirror, vault, inbox). Vault location is selectable at init and auto-detected from existing `<workspace>/work/vault` or `<workspace>/vault`; the separate vault repo entry is skipped when the vault lives inside work (e.g. as a submodule). Cloud mirror is selected at init from detected mounts (Dropbox preferred, Google Drive accounts are listed); shell exports `CLOUD_WORKSPACE`/`CLOUD_WORK`, alias `cwork`, and the `ws()` jumper (formerly `GDRIVE_*`/`gwork`) |
| **ai** | full | AI CLI/config helpers, Claude/Codex/Copilot/Kiro/Kimi/Qwen/pi/Antigravity/Aider/Maru settings backup, optional HUD |
| **fonts** | full | Nerd Font download from GitHub Releases |
| **macapps** | full (darwin) | Install selected Homebrew casks from the embedded catalog |
| **conda** | full | Conda/Mamba `.condarc` defaults; shell hooks live in managed shell init |
| **gpg** | full | GPG agent + git commit signing |
| **secrets** | full | Age-encrypted SSH keys and shell secrets |


The `ai` command groups related operations under one path:

| Previous command | Canonical command |
| --- | --- |
| `dot ai rollback <id>` | `dot ai family rollback <id>` |
| `dot ai status` | `dot ai backup status` |
| `dot ai list` | `dot ai tools list --details` |
| `dot ai memory install` / `status` | `dot ai mcp install amem` / `status amem` |
| `dot ai vault install` / `status` | `dot ai mcp install vmem` / `status vmem` |
| `dot ai research install` / `status` | `dot ai mcp install ares` / `status ares` |

Previous paths remain hidden compatibility commands for one release and print
the replacement on stderr. `ai memory audit`, `archive` and retirement commands
keep their paths. `ai tools list --json` keeps its existing catalog schema;
`--details` is the human inventory and `--include-auth` adds credential file
names to that inventory without reading credentials.

## Profiles

Profiles use YAML inheritance. `full` extends `minimal`.

| Profile | Modules | Packages | Use Case |
|---------|---------|----------|----------|
| **minimal** | 5 | 17 | Lightweight dev setup |
| **full** | 14 | 28 | Complete workstation (macapps enabled on darwin) |
| **server** | 8 | 21 | GPU/DGX server |

**server**: Extends `minimal` + tmux, ai, conda. Disables workspace, fonts, macapps, gpg, secrets. Auto-suggested when NVIDIA GPU or CUDA is detected.

---

## Architecture

Same modular Go architecture as [rootfiles](https://github.com/STAIxBWLB/rootfiles).

Package map and data flow: [docs/guides/architecture.md](docs/guides/architecture.md).

```
rootfiles (root, server)        dotfiles (user, workstation)
━━━━━━━━━━━━━━━━━━━━━━━━━━━     ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Packages (APT), users, SSH       Packages (Homebrew), shell, git
Docker, GPUs, tunnels            Terminal, fonts, AI
Locale, firewall, storage        Workspace, secrets, sync, tmux
```

## CI/CD

### Test Pipeline

| Job | Matrix | Description |
|-----|--------|-------------|
| **decision** | ubuntu-latest | Skips the jobs below when only non-test files changed, or when a main push carries a tree its PR already validated (`scripts/ci-decision.sh`) |
| **release-notes** | ubuntu-latest | Format check of every `docs/releases/v*.md` (`scripts/check-release-notes.sh`); always runs, in seconds |
| **lint** | ubuntu-latest | golangci-lint |
| **unit** | ubuntu-latest, macos-latest | Go unit tests + coverage |
| **integration** | ubuntu-24.04 × {minimal,full,server} + server image | Docker-based profile tests |
| **linux** | shards `apply`, `idempotency`, `rest` on the ubuntu-22.04 image | Module and E2E scenario suite, split from one full list |
| **apps-install-macos** | macos-latest | macOS cask install plus macapps scenario |
| **ci-ok** | ubuntu-latest | Fails when any job above did not succeed; heavy jobs may be skipped only by the decision job. The only check branch protection requires |

Test runs on pull requests and main pushes, not on tags. A Homebrew or download failure inside the integration and module containers is marked `Infrastructure (network)` and retried once (`scripts/ci-net-retry.sh`).

**Vulnerability scan** (`vuln.yaml`): govulncheck through `scripts/govulncheck-gate.sh`, daily and on changes to `go.mod`, `go.sum` or the allowlist, so a new upstream advisory does not block unrelated PRs. Dependabot raises Go modules and actions weekly.

**Release**: Triggered by a `v*` tag push. It waits for the tagged commit's main-push Test run and publishes only when that run succeeded, so the tag is not tested again. It runs the govulncheck gate before publishing. Uses GoReleaser for cross-platform builds (darwin/linux × amd64/arm64), pushes the cask to the tap's `publish/dotfiles-<version>` branch (the tap audits and merges it), and attests build provenance for every archive in `checksums.txt` (`gh attestation verify <archive> -R STAIxBWLB/dotfiles`).

### Creating a Release

Add `docs/releases/vX.Y.Z.md` (format: `AGENTS.md`, Release notes) and merge it first; the Release workflow publishes it as the release body and stops when it is missing or breaks the format. Check it locally with `bash scripts/check-release-notes.sh docs/releases/vX.Y.Z.md`: plain first line, only the `### New` / `### Improved` / `### Fixed` / `### Action needed` sections in that order, every bullet ending with its PR number, no commit SHAs or conventional-commit prefixes, and the compare link to the previous release as the last line.

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
# Release waits for the main-push Test run of that commit, then publishes
```

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
