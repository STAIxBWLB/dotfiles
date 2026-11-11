# Maintenance, backup and migration

## Updating dot

`dot update` updates dot itself; `dot ai update` updates the AI CLIs and plugins. `dot update --check` reports the latest version and which path an update would take; `--dry-run` prints the brew commands of the full maintenance gate; `--wait 30m` waits for a busy maintenance slot instead of deferring at once.

**Homebrew install** (the resolved binary sits in the Cellar for the formula or in the Caskroom for the cask): `dot update` upgrades through the brew that owns it and never writes the Cellar or Caskroom itself.

- A verified native release with the exact trusted tap recipe (`Formula/dotfiles.rb` or `Casks/dotfiles.rb`) takes the bounded prebuilt path under [Resource admission](#resource-admission-dot-admit).
- A stale or changed recipe takes the full maintenance gate. It runs `brew update` only when the tap lags the GitHub release, then `brew upgrade --formula <tap>/dotfiles` (`--cask` for the cask), and checks the new version.
- A new release reaches the tap a few minutes after GitHub, once the tap's audit passes. Until then `dot update` says the tap does not offer the new version yet; run it again shortly.
- A stale local tap clone (the cask it reads is not the canonical release cask) also sends the update through the full maintenance gate; `brew update` first restores the prebuilt path.
- `dot update` reads releases through the GitHub API; when it fails with HTTP 403 on the unauthenticated rate limit, set `GITHUB_TOKEN` (for example `GITHUB_TOKEN=$(gh auth token) dot update`).
- A pinned formula is left alone with a `brew unpin` hint. A standalone `dot` can target an existing managed Homebrew install with `dot update --homebrew`.
- `dot update --version` and `dot rollback` refuse on Homebrew, which owns its versions, and name the brew commands to use instead.

**Moving to the cask:** the `dotfiles` cask (the `dot` binary plus the `dotfiles` back-compat link) replaces the earlier formula, and the tap moved to `staixbwlb/cask` in October 2026.

- An install from the formula moves to the cask on the next `brew update` or `dot update` (the tap's `tap_migrations.json`). `dot update` then carries on as the cask and lists what still runs the formula's opt link (guard hooks, the noindex agent, the container shim) before you remove the old keg.
- When brew could not move it, for example on a Linuxbrew prefix without a `Caskroom`, `dot update` names `brew uninstall --formula --force dotfiles && brew install --cask staixbwlb/cask/dotfiles`.
- If `brew info dotfiles` names any other tap, run `brew trust staixbwlb/cask`, then `brew reinstall staixbwlb/cask/dotfiles`.
- Homebrew 6 and later refuse a short cask name from an untrusted tap (`Refusing to load cask staixbwlb/cask/dotfiles from untrusted tap`). The full name `staixbwlb/cask/dotfiles` taps and trusts it as it installs. Taps that `dot apply` adds (`staixbwlb/cask`, `manaflow-ai/cmux`, `stablyai/orca`) are trusted automatically.

**LaunchAgents after an upgrade:** launchd fails the next spawn of a dot LaunchAgent after a Homebrew upgrade (`spawn failed`, `needs LWCR update`), because its managed launch constraint did not follow the replaced binary. `dot update` therefore reloads (bootout, then bootstrap) every loaded `com.dotfiles.*` agent that runs the upgraded binary; an unloaded one, such as a paused sync, stays unloaded. After a manual `brew upgrade`, `dot sync status` and `dot peer status` name that state with the reload command: `dot sync pause && dot sync resume` for `com.dotfiles.sync*`, and `dot peer setup` for `com.dotfiles.peer`.

**Standalone install** (curl installer or `make install`):

- `dot update --version 2.71.5` (or `v2.71.5`) selects an exact stable release, including a downgrade. The archive checksum, native architecture and exact runtime version are verified before activation.
- Binaries live in `~/.local/share/dotfiles/tools/dot/<version>/bin/dot`. `current` and `previous` are relative links, and `~/.local/bin/dot` points through `current`. The first update keeps the existing standalone binary (a development build goes to a content-hashed `snapshot-*` directory), so its old path still works through `current`.
- `dot rollback` activates the previous binary without downloading, and another rollback switches back when that release also supports `rollback`. For an older release, run the saved newer binary at `<version>/bin/dot rollback`.
- Activation keeps current, previous and any version a running process uses; older idle versions are pruned by the family-tool retention rule. When a process uses an unresolved `PATH` or `current` entry, or process inspection fails, versions are kept until a later activation.
- `--home X` writes the version tree and `PATH` entry only under X and leaves the invoking user's executable in place.
- Guard hooks use a stable `current` or Homebrew `opt` path, so they follow activation and rollback without rewriting settings.

## Migrate from another machine

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

**Option B — individual commands (advanced; `dot backup` / `dot restore` above are the front door):**

```bash
# On the existing machine
dot profile backup --tag "pre-migration" --include-age-key
dot apps backup                       # also snapshot per-app settings
dot ai backup                         # portable Claude/Codex/Copilot/Qwen/pi/Antigravity/Maru/MCP settings

# On the new machine (Drive already mounted)
dot profile restore --include-age-key # restores ~/.config/dotfiles + ~/.ssh/age_key*
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

## Scheduled AI tool updates

```bash
dot ai update schedule status --json
dot ai update schedule enable
dot ai update schedule disable
```

Scheduling is opt-in; setup never installs or enables the service. On macOS a user LaunchAgent runs stable updates on Sunday at 04:00 local time, with bounded hourly, load and login catch-up checks at most once an hour. Each Sunday window allows at most three scheduled attempts, counting failed, partial and deferred runs; the budget survives restart and re-enable, and once it is spent the hourly checks skip heavy probing until the next window. Status shows the remaining attempts and the next eligible time, and manual updates do not use the budget. `enable` requires selected agents and healthy admission, and waits for it up to `--wait` (default 6m, at most 10m). Schedule changes reject `--home`, because launchd controls the current user's domain. A deferred pass is not an up-to-date result, and rolling out to the peer machine remains a separate per-machine check.

## Resource admission (`dot admit`)

One controller admits heavy work: builds, full test runs, indexing, bulk copies, and dependency and tool updates.

- **Per-repo slot:** one heavy job per project repo by default. The scope is the repo's git common dir, so all its worktrees, branches, agent sessions and runtime profiles share the slot, while different repos run in parallel. `admission.slots.heavy_per_repo` raises the limit, to at most eight.
- **Maintenance slot:** global tool installs and updates take the host-wide maintenance slot instead (`--class maintenance`, `admission.slots.maintenance`, one by default).

`dot admit`, the selected-tooling updates and the scheduled update share the same slots, pressure history and state.

- `dot admit [--project <checkout>] -- COMMAND` exits 75 at once on fresh host pressure. Within `--wait` (default 30m; `--wait 0` fails fast) it waits for a busy slot and keeps sampling through a recovery countdown. `--class maintenance` takes the maintenance slot. The child gets `CARGO_BUILD_JOBS=2`, `RUST_TEST_THREADS=2` and `GOMAXPROCS=2` in place of inherited values; `--no-limit-threads` keeps the caller's settings. `--project` selects both the slot and the child's working directory, and `--dry-run` previews without probes or writes. A job launched inside an admitted job runs in its parent's slot when it asks for the same scope and class.
- `dot admit relax --for 1h --ignore pressure` admits regardless of telemetry until the override expires. `--set thresholds.recover_sustain=1m --set slots.heavy_per_repo=2` layers any policy key (relative to `admission.`, repeatable) for that window and is validated like config. `--for` defaults to 1h and is capped at 24h, `--clear` ends the override at once, and `--dry-run` prints what it would write or clear. An expired override is ignored and removed by the next gate. The override lives in `~/.local/state/dot/admission/override.json`. Limits are resolved again before every sample and slot attempt, so an override that expires or is cleared during `--wait` stops applying to that run. If it ends or changes after the slot is taken but before the job starts, the run re-gates on the limits now in force; with no `--wait` left, it defers (exit 75), names the override and says to rerun.
- `dot admit --ignore pressure,slot,uncovered -- COMMAND` (or `--ignore all`) steps past those rules for one run. `DOT_ADMISSION_IGNORE=<same list>` does the same for `dot ai update` and `dot update`. An unknown rule name is refused wherever it appears in the list. A bypass never hides what it skipped:
  - the gate still samples and records, and prints one stderr line naming the bypassed reasons
  - a slot bypass takes a lease in an overflow slot past capacity (at most eight past it), which `dot admit status` lists
  - the uncovered bypass still scans and prints the jobs it found
  - `--json` adds `ignored` and `ignored_reasons`
- The selected-tooling updates (`dot ai update`, `dot ai tools apply` and the AI module of `dot apply`) take the maintenance slot and defer at once when it is busy; the scheduled pass waits up to 6 minutes for it.

**Host-pressure gate.** New work defers on any of:

- memory pressure warning or critical
- a serious or critical macOS thermal state
- CPU idle below 15%, or load1 at or above the CPU count, sustained for 60 seconds
- a WindowServer watchdog termination in the last 30 minutes

The probes read the memory pressure level, the thermal state, load1 and CPU idle (`/proc` on Linux), plus the WindowServer reports in the system and user `DiagnosticReports` folders and their `Retired/` subfolders. Reports from before the last boot are ignored. A standard (non-admin) account cannot read the system folders, so the scan stays incomplete and the gate defers.

After a pressure episode, admission resumes only after five continuous minutes of normal memory, CPU idle at or above 30% and load1 below 70% of the CPU count. Both windows count only completed samples taken no more than 45 seconds apart. A missing or stale sample, a timestamp rollback or a larger gap resets the streak, and an active defer stays active until a fresh sequence completes. Missing or failed telemetry always defers: the gate never reports a machine it cannot measure as healthy.

**Unslotted heavy work.** Once a slot is held, the wrappers scan the process table for heavy work that holds no slot: builds, tests, installs, bulk copies, or anything at the configured CPU percent (100% by default). They defer when that work runs in the same repository, or when its repository is unknown. For the maintenance slot, install, update, upgrade, sync or CI work in any repository counts. `admission.uncovered` shapes the scan: `enabled: false` skips it, `cpu_percent` moves the CPU-only rule (0 turns it off), and `ignore_names` exempts exact process basenames. Work inside a live slot holder's process tree is not counted. `dot peer sync` and `dot sync push` hold a background lease while they run, so their rsync never defers other work. The scan is a bounded heuristic, not proof that every process is controlled.

**Defer, leases and status.**

- On defer, `dot admit` exits 75 (`EX_TEMPFAIL`), and `--json` prints `scope`, `owner`, `reason`, `retry_after`, plus `ignored` and `ignored_reasons` when a rule was stepped past.
- The alert goes through the watchdog notifier at most once per defer episode per scope.
- A lease carries the owner's pid, start time and a 30-second heartbeat. A crashed owner's slot is reclaimed only after its heartbeat expires and its process is verifiably gone, so PID reuse cannot steal a slot.
- `dot admit status` shows:
  - the owners of each slot
  - every effective limit and its source (`default`, `config` or `relax`)
  - an active relax and when it ends
  - per-probe availability, the last WindowServer event and the recovery countdown

  It also notes that jobs started outside admission hold no lease. `--json` carries the limits as `limits`, with `relax_until`, and `resolve_error` when the config or override could not be resolved (the verdict then reflects the defaults).
- State lives in the real user's `~/.local/state/dot/admission/`, and `--home` does not partition it. The controller never kills processes or restarts services: it defers, it does not enforce.

**Limits.** Every value above is a default. The `admission` block in `~/.config/dotfiles/config.yaml` overrides single keys under `thresholds`, `prebuilt`, `slots` and `uncovered`; [Configuration](configuration.md#configuration) lists the keys and their validation. An invalid value stops the command, naming the key and the file. Resolution order:

1. one-off `--ignore` (the flag, then the environment variable)
2. an active `dot admit relax` override
3. config
4. the defaults

Only the ignore set steps past a rule, and a slot bypass still holds an overflow lease.

```bash
dot admit -- make test                                 # one heavy slot per repo
dot admit --class maintenance -- brew upgrade          # the single host-wide maintenance slot
dot admit --project . --wait 6m -- cargo test -- --test-threads=2
dot admit relax --for 1h --ignore pressure             # time-bounded pressure bypass
dot admit relax --for 2h --set thresholds.defer_sustain=30s --set slots.heavy_per_repo=2
dot admit --ignore slot -- cargo build                 # step past a busy slot, in an overflow lease
dot admit status                                       # owners, pressure evidence, effective limits, hysteresis
```

`dot update` has a narrowly scoped exception for a verified native release prebuilt. It still takes the host-wide maintenance slot. Memory warning is tolerated only after 30 seconds of fresh healthy samples with no swap-in or swap-out; samples more than 45 seconds apart, missing telemetry, critical or unknown memory, serious/critical thermal state, recent WindowServer watchdog, idle below 30%, load1 at or above 70% of CPU count, or any swap activity defer the update. This policy has its own history and does not modify heavy-work admission history. The release must be the exact stable GitHub asset for the native platform, match `checksums.txt`, contain one root `dot` executable and at most the bounded root `LICENSE` and `README.md`, and report the requested version. Limits are 32 MiB compressed archive, 64 MiB binary, 1 MiB per release document, 67 MiB for the entire expanded gzip stream including tar metadata and padding, and 1 MiB for `checksums.txt`. For Homebrew, the installed binary and keg (or Caskroom) version, exact raw recipe under the trusted tap, native URL, checksum, target version, and unpinned state (formula only) must match. The warning-tolerant path also requires Homebrew's existing portable Ruby and Bundler to be ready: the version marker must match the `portable-ruby/current` link and both binaries must be executable. Both default and XDG Homebrew `brew.env` locations are scanned; any active `HOMEBREW_` assignment, Ruby-mode or XDG process override, or relative Homebrew config path sends the operation through the full maintenance gate. Before and after admission, dot checks the recipe, pin, installed version, runtime readiness, the running updater's native architecture, and the current Homebrew opt binary's native architecture. After `brew upgrade`, it verifies both the installed version and native architecture. The canonical recipe has no dependencies; the formula has no post-install hook, and the cask's only hook removes the macOS quarantine attribute from its own `dot`. Any unverified recipe or stale metadata uses the full maintenance gate. A standalone binary can target an existing Homebrew install with `dot update --homebrew`; Homebrew performs the managed update and dot does not write the Cellar directly. This exception does not cover generic refreshes, installs, or other tool updates. `admission.prebuilt` changes this profile's idle and load thresholds and its `safety_window` (30 seconds by default).

---

## Setup and safe application

`dot init --from <file>` pre-populates identity from another machine, while workspace path and terminal are confirmed locally. Setup covers identity, timezone, profile, GPU detection, module opt-ins, SSH key, workspace repositories, vault location, and GitHub authentication.

For a cautious apply, run `dot init --check-only`, `dot init --detect`, `dot diff --summary`, and `dot apply --dry-run` before a selected module. Files are backed up under `~/.local/share/dotfiles/backup/` before overwrite; an identical SHA256 is not overwritten. `dot config export <file>` creates the portable YAML consumed by `dot init --from <file>`. Updating dot itself: [Updating dot](#updating-dot).

## Secrets and migration

`dot secrets restore` decrypts to a 0600 temporary file and atomically renames it into place. A differing target is retained as `<dest>.bak-<timestamp>`; identical files remain unchanged. `dot backup` and `dot restore` are the front door: they coordinate profile, apps, AI, and secrets, can run `dot apply` after profile state, and retain pre-restore copies. The per-domain `profile`, `apps`, `ai` and `secrets` backup/restore commands are the advanced path for one domain. `--include-age-key` adds the plain `~/.ssh/age_key*` identity to a profile snapshot; the `secrets` domain is the encrypted `.age` files. `dot profile prune` and `dot ai prune` each prune only their own snapshot tree.

## Cleanup and indexing

`dot clean` always protects `_sys/`. Its safe set includes node modules, Python caches/environments, `.next`, `.cache`, and `.DS_Store`; `--all` adds `dist`, `build`, `out`, and `target` because those can hold deliverables.

`dot noindex` walks project roots but marks cache roots at the top. `~/.claude` is excluded so plans and skills stay searchable. `build` and `out` are not marked because finished deliverables need to be findable. A marker affects only future Spotlight indexing; use `sudo mdutil -E /` to remove existing entries.

## Watchdog operation

`dot watchdog` ships disabled in every profile; a host opts in with `watchdog.enabled: true` and `dot watchdog setup`, which writes a resolved config snapshot to `~/.local/state/dot/watchdog/watchdog.yaml` and loads the user LaunchAgent `com.dotfiles.watchdog.reap`. The scheduled `dot watchdog reap` reads only that snapshot, so profile edits take effect on the next `setup`, not the next pass. "Sustained" CPU is measured across runs via `samples.json`, keyed by PID plus process start time so PID reuse cannot inherit a streak, and a below-threshold sample resets it. `dry-run` mode only logs and notifies; `enforce` sends SIGTERM, waits `kill_grace`, then SIGKILLs. Every candidate and kill lands as a JSON line in `~/Library/Logs/dot/watchdog.log`; `dot watchdog log [N]` tails it. `dot watchdog uninstall` removes the LaunchAgent; deleting state and logs is an interactive-only prompt that `--yes` never auto-confirms.

With `watchdog.warp.enabled: true`, setup also installs the root LaunchDaemon `com.dotfiles.watchdog.warp` (the daemon restart needs the system domain; setup primes sudo and refuses when the Cloudflare WARP daemon is not in `launchctl list`). Each `dot watchdog warp` pass counts health as `warp-cli` Connected AND an interface address in 100.96.0.0/12. After `fail_threshold` consecutive failures it runs `warp-cli disconnect && connect` (up to 3 attempts), then `launchctl kickstart -k` the WARP daemon, rate-limited to one restart per 30 minutes; the bookkeeping lives in `warp.json` and every action is logged and notified. `dot watchdog setup --headless` (requires `watchdog.power.headless: true`) saves the current `pmset`/`systemsetup` values to `power.json` BEFORE applying `pmset -c sleep 0`, `pmset -a autorestart 1`, `pmset -a womp 1`, and `systemsetup -setrestartfreeze on`; uninstall removes the warp daemon and offers to restore those values under the same interactive-only prompt style as the state deletion. `dot watchdog status` also reports the heal daemon, live WARP health, and whether the `dot tunnel` cloudflared daemon is installed — the independent SSH path a remote host should carry.

With `watchdog.monit.enabled: true`, setup ensures the `monit` binary (offering `brew install monit` when missing), renders `~/.config/monit/monitrc` (0600), and loads the user LaunchAgent `com.dotfiles.monit`, which keeps monit running in the foreground on a 60-second cycle with its web UI off. The monitrc alerts through `dot watchdog notify warn` when loadavg(5min) exceeds `load1_threshold` (default: the host's logical CPU count) or cpu(user)/cpu(system) exceed their thresholds (defaults 90%/50%) for `cycles` consecutive checks (default 3), logging to `~/Library/Logs/dot/monit.log`. When `watchdog.monit.screensharing` is on (the default), setup asks — before rendering the monitrc — whether to install, with sudo, the root-owned helper `/Library/Application Support/dot/screensharing-heal` plus a `/etc/sudoers.d/` grant (validated by `visudo -c -f`) letting the user run exactly that helper passwordless; monit's `check host screensharing` then kickstarts `com.apple.screensharing` when the VNC handshake on 127.0.0.1:5900 fails, and declining the helper leaves the check out of the monitrc entirely. `dot watchdog status` reports the monit binary, the agent's loaded state, and monitrc drift against the snapshot; uninstall removes the agent and monitrc, and offers the root helper and sudoers removal under the same interactive-only, default-No prompt as the power restore.

With `watchdog.beszel.enabled: true`, setup adds the Beszel external-monitoring agent: it verifies `watchdog.beszel.hub_url` is set and the env file `~/.config/beszel/agent.env` exists BEFORE any Homebrew prompt — a missing hub URL or env file skips the step with a warning, since the env file carries the hub KEY/TOKEN and arrives via `dot secrets restore` — then ensures the `henrygd/beszel/beszel-agent` Homebrew formula (homebrew-core carries no `beszel-agent`) and installs the user LaunchAgent `com.dotfiles.beszel-agent`. The unit is deliberately dot-owned rather than a `brew services` unit: it runs `/bin/sh -c 'set -a; . "$HOME/.config/beszel/agent.env"; set +a; export LISTEN="${LISTEN:-<listen>}"; exec <agent binary>'` so the secrets-managed env (HUB_URL/KEY/TOKEN/LISTEN) is injected at start, with `watchdog.beszel.listen` (default `:45876`) only the fallback — an env-file LISTEN wins over the config value. A custom `watchdog.beszel.env_path` works the same way but is NOT covered by `dot secrets` backup/restore (only the default path is a registered secrets pair); setup says so and its missing-file skip asks for manual placement instead of `dot secrets restore`. `dot watchdog uninstall` removes the plist while always keeping the env file. `dot watchdog status` reports the hub URL from the snapshot, env file PRESENCE only (never contents), the plist state, and the agent binary. Alert rules — agent offline ≥ 5 minutes, CPU > 80% for 10 minutes, elevated load average — are configured on the external Beszel hub itself; hub-side alerting is an ops concern, not config dot ships.

With `watchdog.notify.telegram.enabled: true`, the watchdog notifier gains a third channel beside macOS (`macos`) and ntfy (`ntfy_url`): a Telegram Bot API `sendMessage` POST. The bot credentials never live in the tracked profile or the synced workspace — they are read, lazily at every send, from the secrets-managed env file `~/.config/dot/telegram.env` (a registered `dot secrets` pair; KEY=VALUE lines, `#` comments and an optional `export ` prefix tolerated) holding `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`, with the same-named process environment variables winning over file values. `watchdog.notify.telegram.env_path` relocates the file (a custom path is not covered by `dot secrets` backup/restore), `reminder_interval` (default 30m) is the minimum time between repeat alerts while a failure persists, and `quarantine_threshold` (default 0, off) caps how many quarantined deletions one peer run may move before warning. Enabled-but-unconfigured (no credentials in the file or the environment) is a clean no-op; a configured channel that fails to send is an error, matching the existing notifier contract.

## Apps and profile snapshots

`dot apps` manages cask installation and per-host settings. A profile snapshot contains config, install and backup lists, metadata, and optional age keys at `<backup-root>/profiles/<hostname>/<version>/`.

## Backup-root resolution

`dot apps`, `dot profile`, and `dot ai` share this precedence: an explicit command flag, saved `BackupRoot`, a cloud root detected from a `secrets/` marker, then the local default. It spans commands and has no generated page.
