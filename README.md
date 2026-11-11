# dotfiles

[![Test](https://github.com/STAIxBWLB/dotfiles/actions/workflows/test.yaml/badge.svg)](https://github.com/STAIxBWLB/dotfiles/actions/workflows/test.yaml)
[![Release](https://github.com/STAIxBWLB/dotfiles/actions/workflows/release.yaml/badge.svg)](https://github.com/STAIxBWLB/dotfiles/actions/workflows/release.yaml)

`dot` is one Go binary that sets up and maintains a user environment on macOS,
Linux workstations and GPU servers: packages, shell, git, terminal, tmux
workspaces, AI agent tooling, secrets and machine-to-machine sync.

## Install

```bash
brew install staixbwlb/cask/dotfiles   # macOS or Linuxbrew
```

Without Homebrew, the installer sets up Homebrew (Linuxbrew on Linux), downloads
the `dot` binary and puts it on `PATH`:

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash
```

Use the full cask name: Homebrew 6 and later refuse the short name from an
untrusted tap (run `brew trust staixbwlb/cask` first if you want
`brew install dotfiles`). Updates, rollback and moving an older install to the
cask: [Updating dot](docs/guides/maintenance.md#updating-dot).

## First run

```bash
dot              # welcome screen with next steps
dot init         # name, email, profile and modules (interactive)
dot apply        # apply the enabled modules
dot update       # update dot itself
dot open myproj  # launch or resume a tmux workspace
```

Moving from another machine: run `dot backup` there and `dot restore` here
([details](docs/guides/maintenance.md#migrate-from-another-machine)).

## Documentation

| Guide | Covers |
|---|---|
| [AI agents](docs/guides/agents.md) | Selected agents and add-ons, work policies, shared context, the agent-hub family, memory, guard hooks |
| [Sync and peer](docs/guides/sync.md) | `dot sync` to a mirror, secret policy, conflicts, `dot peer` between two machines |
| [Workspaces and tmux](docs/guides/workspace.md) | `dot open`, layouts, `dot hub`, key bindings, themes, `dot ws` |
| [Configuration](docs/guides/configuration.md) | The config file, environment variables, prompt styles, terminal apps, packages |
| [Maintenance](docs/guides/maintenance.md) | Updates, backup and migration, `dot admit`, watchdog |
| [Servers and containers](docs/guides/servers.md) | GPU server provisioning, `dot container`, Cloudflare Tunnel |
| [Architecture](docs/guides/architecture.md) | Package map and data flow |

Also: the [command reference](docs/commands/) (generated from `--help`), the
files `dot` may write ([BOUNDARIES](docs/BOUNDARIES.md)), accepted design
limits ([CEILINGS](docs/CEILINGS.md)), design specs ([docs/specs](docs/specs/))
and [release notes](docs/releases/).

## Modules

Applied in this order. The profile column names the smallest profile that
enables the module.

| Module | Profile | What it manages |
|---|---|---|
| packages | minimal | Homebrew formulae |
| shell | minimal | zsh, Oh My Zsh, plugins, shell config |
| node | minimal | pnpm store outside cloud-synced workspace trees |
| git | minimal | git config, aliases, global ignore |
| ssh | minimal | SSH config and `config.d` includes |
| terminal | minimal | starship prompt, Orca (macOS/Arch), Warp theme |
| container | opt-in | one `container` command: apple/container on macOS, docker or podman on Linux |
| tmux | full | tmux.conf (C-a prefix, vim keys) |
| workspace | full | work and vault repos, `gh` auth, links to the cloud mirror, vault and inbox, the `ws` jumper |
| ai | full | AI CLIs, their settings and backups, optional HUD |
| fonts | full | Nerd Fonts from GitHub Releases |
| macapps | full (macOS) | selected casks from the embedded catalog |
| conda | full | `.condarc` defaults (its shell hooks live in the managed shell init) |
| gpg | full | GPG agent and commit signing |
| secrets | full | age-encrypted SSH keys and shell secrets |

## Profiles

`full` and `server` extend `minimal`.

| Profile | Modules | Packages | For |
|---|---|---|---|
| minimal | 6 | 17 | a lightweight dev setup |
| full | 14 | 28 | a complete workstation |
| server | 9 | 21 | a GPU server: minimal plus tmux, ai and conda |

`dot init` suggests `server` when it detects an NVIDIA GPU or CUDA. `container`
is off in every profile until `dot container setup` enables it.

## Development

Go 1.26.11 or newer (`go.mod`).

```bash
make build     # bin/dot
make test      # go test ./... -race
make lint      # golangci-lint
make docs      # regenerate docs/commands; CI fails when it drifts
make install   # ~/.local/bin/dot plus the dotfiles link
```

Work on a branch and open a PR; `AGENTS.md` holds the contribution rules. The
system-level counterpart for root and servers is
[rootfiles](https://github.com/STAIxBWLB/rootfiles).

## CI and release

The Test workflow runs on pull requests and main pushes:

| Job | Runs on | Checks |
|---|---|---|
| decision | ubuntu | skips the heavy jobs when only non-test files changed, or when a main push carries a tree its PR already validated |
| release-notes | ubuntu | the format of every `docs/releases/v*.md` |
| lint | ubuntu | golangci-lint |
| unit | ubuntu, macOS | Go unit tests with coverage |
| integration | ubuntu-24.04 × minimal, full, server, plus the server image | Docker-based profile tests |
| linux | three shards on the ubuntu-22.04 image | module and end-to-end scenarios |
| apps-install-macos | macOS | cask install and the macapps scenario |
| ci-ok | ubuntu | fails when any job above did not succeed; the only required check |

A Homebrew or download failure inside the test containers is retried once and
labeled `Infrastructure (network)`. The Vulnerability workflow runs govulncheck
daily and when `go.mod`, `go.sum` or the allowlist change; Dependabot raises Go
modules and actions weekly.

To release, merge `docs/releases/vX.Y.Z.md` (format in `AGENTS.md`, checked by
`bash scripts/check-release-notes.sh`), then tag the merge commit:

```bash
git tag vX.Y.Z && git push origin vX.Y.Z
```

The Release workflow waits for that commit's main-push Test run, runs the
govulncheck gate, builds darwin and linux for amd64 and arm64 with GoReleaser,
publishes the notes as the release body, attests every archive
(`gh attestation verify <archive> -R STAIxBWLB/dotfiles`) and pushes the cask to
the tap's `publish/dotfiles-<version>` branch, which the tap merges after its
audit, usually a few minutes later.

## License

MIT
