# Maintenance, backup and migration

## Updating dot

`dot update` updates dot itself; `dot ai update` updates the AI CLIs and plugins. `dot update --check` reports the latest version and which path an update would take; `--dry-run` prints the brew commands of the full maintenance gate; `--wait 30m` waits for a busy maintenance slot instead of deferring at once.

**Homebrew install** (the resolved binary sits in the Cellar for the formula or in the Caskroom for the cask): `dot update` upgrades through the brew that owns it and never writes the Cellar or Caskroom itself.

- A verified native release with the exact trusted tap recipe (`Formula/dotfiles.rb` or `Casks/dotfiles.rb`) takes the bounded prebuilt path under [Resource admission](#resource-admission-dot-admit).
- A stale or changed recipe takes the full maintenance gate. It runs `brew update` only when the tap lags the GitHub release, then `brew upgrade --formula <tap>/dotfiles` (`--cask` for the cask), and checks the new version.
- A new release reaches the tap a few minutes after GitHub, once the tap's audit passes. Until then `dot update` says the tap does not offer the new version yet; run it again shortly.
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

## Resource-Safe Maintenance

Builds, full test runs and tool updates go through the resource admission
controller described in [Resource admission](maintenance.md#resource-admission-dot-admit).
The selected-tooling updates and the scheduled update below hold the same
slots as `dot admit`.

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

## Resource admission (`dot admit`)

One controller admits heavy work (builds, full test runs, indexing, bulk copies, dependency and tool updates): one heavy job per project repo by default (`admission.slots.heavy_per_repo` raises it to at most eight), shared across all its worktrees, branches, agent sessions and runtime profiles, while different repos run in parallel. Global tool installs and updates share the host-wide maintenance slots (`admission.slots.maintenance`, one by default) instead. `dot admit` and selected-tooling updates use the same slots, pressure history and state:

- `dot admit [--project <checkout>] -- COMMAND` exits 75 at once on host pressure and waits up to `--wait` (default 30 minutes; `--wait 0` fails fast) for a busy slot; `--class maintenance` takes the maintenance slot. The child receives `CARGO_BUILD_JOBS=2`, `RUST_TEST_THREADS=2` and `GOMAXPROCS=2`, replacing inherited values. `--no-limit-threads` keeps the caller's worker settings. `--project` selects both the slot and the child's working directory; `--dry-run` previews without probes or writes.
- `dot admit relax --for 1h --ignore pressure` admits regardless of telemetry until the override expires; `dot admit relax --for 1h --set thresholds.recover_sustain=1m --set slots.heavy_per_repo=2` layers any policy key (relative to `admission.`, repeatable) for that window, validated the same way as config. `--clear` removes it at once, `--for` defaults to 1h and is capped at 24h, and an expired override is ignored and removed by the next gate. The override lives in `~/.local/state/dot/admission/override.json` with `set_at`, `expires_at`, `ignore` and `set`. `--dry-run` prints the override it would write (or clear) and changes nothing. Every gate sample and every slot attempt resolves the limits again, so an override that expires or is cleared during a `--wait` stops applying to that run — including mid-recovery: a closed episode is persisted, so a relaxed `recover_sustain` that outlives its override would otherwise end the episode under limits the controller no longer holds. A probe can also outlast the override that set the limits it is judged against, and that sample is discarded rather than recorded: the run resamples with the limits now in force.
- `dot admit --ignore pressure,slot,uncovered -- COMMAND` (or `--ignore all`) and `DOT_ADMISSION_IGNORE=<same list>` step past the named rules for one run; the wrapped updates (`dot ai update`, `dot update`) honor the environment variable. A bypass never hides what it skipped: the gate still samples and records the snapshot and prints one stderr line naming the bypassed reasons, the slot bypass takes a lease in the first free overflow slot past capacity instead of running leaseless (`dot admit status` lists it) — it lands past capacity even when a normal index is free, because the notice, the `ignored` set and the resourceguard adapter all key on the index — the uncovered bypass still scans and prints the jobs it found instead of deferring, and `--json` adds `ignored` plus the bypassed reasons, the slot one being the same overflow-slot sentence the notice prints. No step decides on limits that have since left. Each admission records the relax override behind every gate, slot take and scan it acted on; at the commit point (after the last slow step, right before the child starts) it re-resolves the limits, takes its own clock, and requires each recorded override to be unexpired at that clock and still the one in force, compared by expiry and contents, so a rewrite that keeps the same end and changes the limits is caught too. When one ended or changed, the slot goes back and nothing starts: the run re-gates on the limits now in force while `--wait` budget remains, and with none left it defers (exit 75) naming the override that ended or changed and saying to rerun. The resourceguard adapter (prebuilt updates and the slot-following paths) runs the same check before it hands out an acquire and defers with the same reason, so its own retry loop re-resolves.
- The selected-tooling updates (`dot ai update`, `dot ai tools apply` and the AI module of `dot apply`) take the maintenance slot and defer at once when it is busy; the scheduled update pass waits up to 6 minutes for it, and `dot ai update schedule enable` up to its `--wait` (default 6 minutes, at most 10).

A host-pressure gate defers new work on memory pressure warning/critical, a serious or critical macOS thermal state, CPU idle below 15% or load1 at the CPU count sustained 60s, or a recent WindowServer watchdog termination; after such an episode, recovery requires five continuous minutes of normal telemetry. Both the 60-second CPU excursion and 5-minute recovery require completed samples no more than 45 seconds apart. Missing/stale samples, timestamp rollback, or a larger gap reset the streak, and an active defer stays active until a fresh sequence completes. Missing or failed telemetry always defers: the gate never reports a machine it cannot measure as healthy. Once a slot is held, the admission wrappers also scan the process table for heavy work that holds no slot (builds, tests, installs, bulk copies, or anything at the configured CPU percent, 100% by default) and defer when it runs in the same repository or its repository is unknown (for the maintenance slot: install, update, upgrade, sync or CI work in any repository). `admission.uncovered` shapes that scan: `enabled: false` skips it, `cpu_percent` moves the CPU-only rule (0 turns it off) and `ignore_names` exempts exact process basenames. Work inside a live slot holder's process tree is not counted; `dot peer sync` and `dot sync push` hold a background lease while they run (listed by `dot admit status`), so their rsync never defers other work. The scan is a bounded heuristic, not proof that every process on the machine is controlled; the controller never kills other sessions or restarts system services.

`dot update` has a narrowly scoped exception for a verified native release prebuilt. It still takes the host-wide maintenance slot. Memory warning is tolerated only after 30 seconds of fresh healthy samples with no swap-in or swap-out; samples more than 45 seconds apart, missing telemetry, critical memory, serious/critical thermal state, recent WindowServer watchdog, idle below 30%, load1 at or above 70% of CPU count, or any swap activity defer the update. This policy has its own history and does not modify heavy-work admission history. The release must be the exact stable GitHub asset for the native platform, match `checksums.txt`, contain one root `dot` executable and at most the bounded root `LICENSE` and `README.md`, and report the requested version. Limits are 32 MiB compressed archive, 64 MiB binary, 1 MiB per release document, 67 MiB for the entire expanded gzip stream including tar metadata and padding, and 1 MiB for `checksums.txt`. For Homebrew, the installed binary and keg (or Caskroom) version, exact raw recipe under the trusted tap, native URL, checksum, target version, and unpinned state (formula only) must match. The warning-tolerant path also requires Homebrew's existing portable Ruby and Bundler to be ready: the version marker must match the `portable-ruby/current` link and both binaries must be executable. Both default and XDG Homebrew `brew.env` locations are scanned; any active `HOMEBREW_` assignment, Ruby-mode or XDG process override, or relative Homebrew config path sends the operation through the full maintenance gate. Before and after admission, dot checks the recipe, pin, installed version, runtime readiness, the running updater's native architecture, and the current Homebrew opt binary's native architecture. After `brew upgrade`, it verifies both the installed version and native architecture. The canonical recipe has no dependencies; the formula has no post-install hook, and the cask's only hook removes the macOS quarantine attribute from its own `dot`. Any unverified recipe or stale metadata uses the full maintenance gate. A standalone binary can target an existing Homebrew install with `dot update --homebrew`; Homebrew performs the managed update and dot does not write the Cellar directly. This exception does not cover generic refreshes, installs, or other tool updates. `admission.prebuilt` changes this profile's idle and load thresholds and its `safety_window` (30 seconds by default).

```bash
dot admit -- make test                                 # one heavy slot per repo
dot admit --class maintenance -- brew upgrade          # the single host-wide maintenance slot
dot admit --project . --wait 6m -- cargo test -- --test-threads=2
dot admit relax --for 1h --ignore pressure             # time-bounded pressure bypass
dot admit relax --for 2h --set thresholds.defer_sustain=30s --set slots.heavy_per_repo=2
dot admit --ignore slot -- cargo build                 # step past a busy slot, in an overflow lease
dot admit status                                       # owners, pressure evidence, effective limits, hysteresis
```

On defer `dot admit` exits 75 (`EX_TEMPFAIL`) and `--json` prints the machine-readable outcome (`scope`, `owner`, `reason`, `retry_after`, and `ignored` plus `ignored_reasons` when a rule was stepped past). Leases carry pid + start time and a heartbeat, so a crashed owner's slot is reclaimed safely, and a job launched inside an admitted job runs in its parent's slot when it asks for the same scope and class, even when that scope holds several leases.

The values above are defaults. Four blocks under `admission` in `~/.config/dotfiles/config.yaml` override individual keys: `thresholds` (`idle_defer_below`, `idle_recover_at`, `load_defer_frac`, `load_recover_frac`, `defer_sustain`, `recover_sustain`, `ws_grace`, `mem_avail_warn_pct`, `mem_avail_critical_pct`), `prebuilt` (`idle_defer_below`, `idle_recover_at`, `load_defer_frac`, `load_recover_frac`, `safety_window`), `slots` (`heavy_per_repo`, `maintenance`) and `uncovered` (`enabled`, `cpu_percent`, `ignore_names`). Validation runs when the policy resolves: percentages stay within 0-100, load fractions above zero, durations at or above zero, `idle_recover_at` at or above `idle_defer_below`, `load_recover_frac` at or below `load_defer_frac`, `mem_avail_critical_pct` at or below `mem_avail_warn_pct`, slot counts between 1 and 8, and `cpu_percent` at or above zero. An invalid value fails the command and names the key and the config file instead of falling back. A duration written as a plain number is a count of seconds, and one too large to fit a duration is refused instead of wrapping into a shorter one. Resolution is one-off ignore (flag, then environment), then an active `dot admit relax` override, then config, then the defaults — resolved again before every gate and slot attempt, so a change during a wait applies to that run. `dot admit status` lists every effective key with its source (`default`, `config` or `relax`), an active relax with its expiry, and each held slot by its index; `--json` carries the same keys as `limits` with `relax_until`, plus `resolve_error` when the config or override could not be resolved (the verdict then reflects the defaults). Only the ignore set steps past a rule, and the slot bypass still holds an overflow lease. State lives in the real user's `~/.local/state/dot/admission/`; `--home` does not partition it.

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

## Resource admission

`dot admit` enforces the workspace resource policy at per-repo granularity: one heavy job (build, full test, indexing, bulk copy, dependency update) per project repo at a time — the scope is the repo's git common dir, so all worktrees, branches, and sessions of one repo share a single slot — while different repos run in parallel subject to the pressure gate. `admission.slots.heavy_per_repo` and `admission.slots.maintenance` raise a scope's capacity to at most eight concurrent leases; index 0 keeps the unsuffixed slot directory name, and higher indexes and overflow slots add a `-N` suffix. Capacity counts the live leases the scope holds whichever index they sit on, so a lease a bypass left past capacity — or one a lowered capacity or an expired relax override stranded at a higher index — still occupies it, and a slot defer names the limit it hit, including the capacity plus the overflow allowance when the bypass was already in play. The count is repeated once the caller holds its index, because a release and a bypass can fall between the two: a caller that finds the scope fuller than its index allows gives the index back, then defers or retakes an overflow one. A bypassing caller always takes an index at or past capacity, so the overflow notice and the JSON `ignored` rule report every bypass. `--class maintenance` takes the host-wide maintenance slot for global tool installs/updates instead. The gate snapshots memory pressure level, the macOS thermal state (`NSProcessInfo.thermalState`), load1, and CPU idle (macOS; Linux reads /proc), plus the newest WindowServer reports in `/Library/Logs/DiagnosticReports` and `~/Library/Logs/DiagnosticReports`, each with its `Retired/` subfolder (a report whose name contains `watchdog`, or an `.ips` with a `WATCHDOG` termination; the system folders are readable only by admin accounts, so on a standard account the scan stays incomplete and the gate defers), and defers on warn/critical memory, a serious or critical thermal state, idle below 15% or load1 at or above the CPU count sustained 60s, or a WindowServer watchdog termination inside its 30-minute grace (pre-boot evidence is suppressed). Every probe carries an availability flag: failed or missing telemetry defers — the gate is never false-healthy. Once an episode opens, recovery needs five continuous minutes of normal memory, idle at or above 30%, and load1 below 70% of the CPU count before the gate admits again. Slots are atomic `mkdir`s holding a lease (`owner, session, pid, pid_start, pgid, class, cwd, acquired_at, heartbeat_at, deadline`, plus the slot index when it is not 0); a 30-second heartbeat keeps the lease alive, and a crashed owner is reclaimed only after the heartbeat expires AND the pid+start incarnation is verifiably gone (a fresh heartbeat is never force-freed, so PID reuse cannot steal a slot). A child `dot admit` under `DOT_ADMISSION_SCOPE` for the same scope/class runs without re-acquiring; when the scope holds several leases (a configured capacity or an overflow slot), it finds its parent by the lease its process tree descends from, not by "the only lease" on the scope. On defer the exit is 75 with a JSON outcome under `--json`, and the alert goes through the watchdog notifier at most once per defer episode per scope. `dot admit status` shows active owners per slot index, the effective value of every admission key with its source, an active relax with its expiry, per-probe availability, the last WindowServer event, and the hysteresis countdown — and always notes that jobs launched outside admission wrappers hold no lease, and a bounded process-table scan defers when such heavy work runs in the same repo or its repo is unknown. State lives in `~/.local/state/dot/admission/`; the controller never terminates processes — deferral, not enforcement, is the mechanism.

## Apps and profile snapshots

`dot apps` manages cask installation and per-host settings. A profile snapshot contains config, install and backup lists, metadata, and optional age keys at `<backup-root>/profiles/<hostname>/<version>/`.

## Backup-root resolution

`dot apps`, `dot profile`, and `dot ai` share this precedence: an explicit command flag, saved `BackupRoot`, a cloud root detected from a `secrets/` marker, then the local default. It spans commands and has no generated page.
