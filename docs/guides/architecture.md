# Architecture

`dot` is one Go binary (`cmd/dot`) built on cobra. Commands live in `internal/cli`, which parses flags and renders output. Every other package below it does the work, so a command handler stays thin and an engine can be tested without the CLI.

# How a run flows

1. **Configuration**: `internal/config` loads the user state (`~/.config/dotfiles/config.yaml`), merges the selected profile (minimal, full, server) and detects the system. Older settings are rewritten to their current names at load.
2. **Apply**: `dot apply` hands the resolved config to the modules in `internal/module`, in a fixed order. Each module checks what differs, then applies it through `internal/exec`, which honors `--dry-run`. Templates come from `internal/template`. A file is backed up before it is overwritten (`internal/fileutil`).
3. **AI agents**: `internal/aisettings` writes the shared agent instructions and MCP entries and hands skill sharing to Maru, which owns skill sources and links; `internal/aitooling` installs the selected agents and the agent-hub family. `internal/aipolicy` resolves work policies, and `internal/aimemory` audits the agents' own memory.
4. **Sync**: `internal/syncer` drives rsync for `dot sync` (workspace to a local or SSH mirror) and `dot peer` (two machines). Each store lives under `<workspace>/.dotfiles/<store>/` with its own filters, baseline and scheduler.
5. **Heavy work**: `internal/admission` and `internal/resourceguard` give each repository one heavy-job slot. Builds and tests started through `dot admit` wait for it; heavy work started outside it holds no slot and is only caught by a process scan that defers new admissions.

The files `dot` may write are listed in [BOUNDARIES.md](../BOUNDARIES.md), and accepted design limits in [CEILINGS.md](../CEILINGS.md).

# Packages

| Package | Role |
|---|---|
| `cmd/dot` | Entry point: runs the cobra tree and maps errors to exit codes |
| `internal/admission` | Per-repository heavy-job slot and the host health gate behind `dot admit` |
| `internal/aihandoff` | Small development notes shared between local agents |
| `internal/aimemory` | Audit of the agents' native memory stores, and the reversible archive |
| `internal/aipolicy` | Resolves portable work preferences against local capabilities |
| `internal/aischedule` | The opt-in weekly AI maintenance LaunchAgent |
| `internal/aisession` | Supervises native agent processes at turn boundaries |
| `internal/aisettings` | Shared agent instructions and MCP entries, skill sharing through Maru, AI settings backup and restore |
| `internal/aitooling` | Installs and updates the selected agents and the agent-hub family tools |
| `internal/appsettings` | Backup and restore of macOS application settings |
| `internal/claudecfg` | Sole owner of `~/.claude/settings.json`, its lock and its write |
| `internal/clean` | Finds and removes regenerable junk directories for `dot clean` |
| `internal/cli` | The cobra commands: flags, output and exit codes |
| `internal/config` | User state, profiles, system detection and the merged config |
| `internal/config/catalog` | The embedded macOS app catalog for `dot apps` and `dot init` |
| `internal/container` | Apple `container` syntax on every host (`dot container`) |
| `internal/exec` | Command runner with dry-run, and Homebrew helpers |
| `internal/fileutil` | Backups, locks, downloads and archive extraction for the files `dot` installs |
| `internal/ghauth` | GitHub CLI authentication for repository clones |
| `internal/guard` | Claude Code safety hooks: careful warnings and the edit freeze |
| `internal/module` | The apply modules: shell, git, packages, terminal, fonts, AI and the rest |
| `internal/noindex` | Keeps Spotlight out of regenerable build and cache directories |
| `internal/profilesnap` | Versioned snapshots of the dotfiles profile (`dot profile`) |
| `internal/resourceguard` | Worker limits and supervised child processes for admitted jobs |
| `internal/secrets` | The age-encrypted secrets store |
| `internal/sliceutil` | Small generic slice helpers |
| `internal/snapstore` | Shared snapshot directory layout for the backup commands |
| `internal/syncer` | rsync engine for `dot sync` and `dot peer`: stores, filters, baselines, schedulers |
| `internal/template` | Embedded templates rendered into config files |
| `internal/toolversion` | Version ranges and family tool manifests |
| `internal/tunnel` | Cloudflare Tunnel SSH setup (`dot tunnel`) |
| `internal/ui` | Terminal styles, prompts and the init wizard |
| `internal/watchdog` | Runaway-process reaper, WARP heal and host health alerts |
| `internal/workspace` | Project registry and tmux layouts behind `dot open` and `dot hub` |
| `internal/ws` | Directory operations on the workspace and its mirror (`dot ws`) |
| `tools/gendocs` | Regenerates `docs/commands` from the cobra tree |

# Key design

- **Module interface**: `Check()` → `Apply()` — idempotent, dry-run aware
- **Profile inheritance**: YAML `extends` chain with field-level merging
- **go:embed**: Profiles, templates, and scripts compiled into the binary
- **SHA256 hash**: Skip writes when content unchanged, backup before overwrite
- **Non-fatal errors**: Module failures logged, remaining modules continue
- **Platform build tags**: Platform-specific code (xattr, launchd, systemd) via `//go:build`

---
