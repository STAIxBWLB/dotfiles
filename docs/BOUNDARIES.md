# Maru / dotfiles Boundary

`dotfiles` owns selected agent/tool installation orchestration, environment
and documented tool settings. Maru owns shared skill sources, registry,
canonical links and target deployment. Dot delegates selected skill sharing to
Maru's capability-checked CLI; it never copies or rewrites skill trees itself.
Native/plugin/system bundles remain owned by their native installers.

The selectable agent environments are Claude, Codex, Kimi, Qwen, Grok,
OpenCode, Genspark Gencode, pi and Antigravity. The automatic add-on catalog
is limited to ripwire, Open Code Review, GSD and ponytail. Project memory is
amem, which dot registers but does not install (see `~/.kiro/settings/mcp.json`
below); claude-mem is retired and only `dot ai memory retire-claude-mem`
touches what is left of it.
Selection limits managed writes; it does not hide existing globally
discoverable skills from unselected agents. Gencode and pi install through
the managed npm prefix; Antigravity installs `agy` under `~/.local/bin` and
self-updates, so update operations defer rather than reinstall.

## dotfiles May Write

- `~/.claude/CLAUDE.md`
- `~/.claude/settings.json` (HUD statusLine block tagged `# dot-hud`, and only
  when dot owns the existing entry or `--force` is passed; `dot guard`
  PreToolUse hook entries tagged `# dot-guard`; entries owned by other tools
  are never touched). Notably NOT dotfiles-managed: `skillOverrides`,
  `permissions`, `enabledPlugins`, `hooks` outside the `# dot-guard` entries.
  Those belong to Claude Code's own UI (`/skills`, `/config`) or to whichever
  tool installed them.
- `~/.claude/settings.local.json` when explicitly included in auth/local flows
- `~/.claude/.dot-lock` — a PID lock directory serializing writers of
  `~/.claude/settings.json`, held only for the duration of a
  `claudecfg.Mutate` call and removed on every exit path; never taken on a
  read
- `~/.claude/statusline-dot.py`
- `~/.claude/keybindings.json`
- `~/.config/claude/**`
- `~/.config/git/gitignore.global` — the global git excludes file
  (`core.excludesFile`), deployed whole by the `git` module from
  `internal/template/templates/git/gitignore.global`. The legacy
  `~/.config/git/ignore` it supersedes is removed by `dot apply`, and only
  when the file still matches the last managed content byte for byte; a
  locally edited copy is left alone
- `~/.config/shell/30-ai.sh`
- `~/.maru/settings.json` and `~/.maru/sites.json` only during explicit AI
  backup/restore operations
- the global AGENTS fan-out targets, one per registered tool:
  `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`,
  `~/.kiro/steering/AGENTS.md`, `~/.kimi-code/AGENTS.md`,
  `~/.pi/agent/AGENTS.md`, `~/.qwen/AGENTS.md`, `~/.gemini/GEMINI.md`,
  `~/.copilot/copilot-instructions.md`, `~/.aider.conf.md`, and
  `~/.config/opencode/AGENTS.md`. Each is
  the whole file, rendered from the agents SSOT; registering a tool adds
  a target here or the boundary test fails. Session transcripts
  (`~/.pi/agent/sessions/**`, `~/.qwen/projects/**/chats/**`) are
  read-only scan surfaces (`amem ingest` reads them; dot never does) and are
  never written. Selected Codex and Kimi targets honor `CODEX_HOME` and
  `KIMI_CODE_HOME`; OpenCode honors `OPENCODE_CONFIG_DIR`, then
  `XDG_CONFIG_HOME/opencode`. Explicit `--home` isolates these destinations
  from ambient profile overrides.
- `~/.qwen/settings.json` — only the additive `context.fileName` entry required
  to discover AGENTS.md; preserve existing filenames and unrelated settings

Dot-owned state trees:

- `~/.local/share/dotfiles/ai/policy/`: private owned Claude launch overlays,
  preference ownership receipts and mutation locks. Only the overlay's
  `permissions.defaultMode`, exact knowledge-tool `permissions.allow`, model
  and effort fields are generated; native
  `~/.claude/settings.json` retains its original ownership limits. Apply and
  rollback refuse foreign edits and symlink paths. These are machine-local
  derivatives of portable `modules.ai.policy`, not shared policy sources.
- `$CODEX_HOME/dot-policy-*.config.toml` (or `~/.codex/` without an override):
  dedicated generated native profiles containing model, effort, approval,
  sandbox settings and per-tool approval leaves for registered Obsidian and
  amem bindings. They do not register servers or copy command/URL/auth
  definitions. Only files recorded in the local policy ownership receipt
  may be replaced or removed. Existing `config.toml`, legacy inline profiles,
  MCP server definitions and authentication stay untouched. Explicit `--home`
  ignores ambient profile locations.
- `~/.local/share/dotfiles/ai/sessions/`: private session receipts and curated
  checkpoints. Receipts contain no raw prompts or model output. Native session
  history remains agent-owned. A managed process may write its normal native
  session history; dot never copies it between providers or machines.

- `~/.config/dotfiles/agents`: the shared agents instruction SSOT
  (`AGENTS.md`), written by `dot ai agents init|pull|author|edit` (edit
  scaffolds a missing SSOT), by `dot apply` (scaffolds it on a fresh
  machine), by `dot ai restore|import`, and by the instruction blocks of `dot ai
  coauthor-guard` and `dot ai agents apply` (the amem memory block, which also
  removes the retired claude-mem block, and only on a machine that uses amem:
  `amem` on PATH, an amem server in a managed MCP config, or the block already
  present; the SSOT is copied to `backup/agents-ssot/<timestamp>/` first, and a
  malformed marker is reported as a warning and never fails the apply), and the selected setup continuity
  policy block (backed up before upgrading an existing SSOT). This dir is synced between
  machines, so it holds no machine state: every agents apply removes the
  legacy apply-state file (`.state.json`) from it, best-effort, after
  migrating the entries that match this machine's targets
- `~/.local/share/dotfiles`: dot's machine-local data tree, holding the
  append-only AI audit log (`ai/events.jsonl`, one record per `dot ai`
  mutation), the agents apply state (`agents/state.json`, the
  last-applied hash of each target on this machine), and the timestamped
  backup trees (`backup/agents*/<timestamp>/...`) taken before agents
  SSOT and target edits. The apply state is written by every agents
  apply: `dot ai agents apply`, `dot apply`, `dot ai coauthor-guard
  apply --apply-agents`, and `dot ai restore
  --reapply-agents`

- `~/.local/share/dotfiles/ai/handoffs/<canonical-git-common-dir-hash>.jsonl`
  — append-only local curated development handoffs shared across project
  worktrees. Records preserve producer claims, UTC time, commit/worktree and
  artifact digests. Show marks stale evidence for revalidation; neither command
  expands raw transcript collection or publishes records externally.
- `~/.local/share/dotfiles/shims/container` — the `container` shim (POSIX
  sh, mode 0755) and `~/.local/share/dotfiles/container/state.json` — this
  host's own choice: the chosen backend, its binary and version, or that the
  host declined. Both are written by `dot container setup`, and by `dot apply`
  when `modules.container` is enabled and the host chose yes (a no writes only
  the declined `state.json`). The file stays out of the synced config, so the
  synced opt-in never installs a backend on a host that did not choose it. The shim
  directory goes on PATH through the shell module's `00-exports.sh`. Setup
  also removes `~/.config/shell/45-container.sh`, only when it still matches
  the old hand-written workaround byte for byte
- `~/.local/share/dotfiles/ai/tooling-state.json` — private machine-local
  installation receipts: adopted provider/path/version/status/check time.
  Native provider files remain native-installer owned.
- `~/.local/share/dotfiles/ai/update-schedule.json`,
  `update-schedule.lock`, `update-schedule.out.log` and
  `update-schedule.err.log` in that directory — selected maintenance schedule
  state, nonblocking state-update lock and launchd run diagnostics
- `~/Library/LaunchAgents/com.dotfiles.peer.plist`: the peer scheduler,
  written by `dot peer setup` on the coordinator and removed by `dot peer
  setup --off`, a handover, a fence demotion and a peer `dot sync owner
  --set`/`--clear` that takes the coordinator role from this Mac.
- `~/Library/LaunchAgents/com.dotfiles.ai.update.plist` — the macOS user
  stable-update job, owned by `dot ai update schedule enable|disable`
- `dot ai run`, the selected-tooling updates and `dot ai update schedule
  enable` hold their slots in the admission state root below (the same slots
  and pressure history as `dot admit`, #162). The former
  `/tmp/dotfiles-resource-<uid>/` store is no longer written.

Third-party files dot edits (each entry states what dot writes there and
under what condition; everything else in the file belongs to its owning
tool):

- `~/.config/git/config` — the `[hook "coauthor-guard"]` table
  (`command = ~/.config/git/hooks/commit-msg`, `event = commit-msg`),
  applies a warn or block mode and the installed git supports config-based
  hooks (2.54+). Configured hooks run in addition to `.git/hooks/*` and any
  repo-local `core.hooksPath`, so neither is touched; a `core.hooksPath`
  dot does not manage is left alone and is preserved across the git
  module's wholesale template rewrite of this file, while a leftover
  dot-managed one (`~/.config/git/hooks` from the pre-2.54 wiring) is
  removed as migration. dot never sets a global `core.hooksPath`. The same
  table is also rendered by the git module's `git/config.tmpl` when
  `modules.git.coauthor_guard` is set. No other table or key is touched.
- `~/.config/git/hooks/commit-msg` — the coauthor-guard hook script,
  written by `dot ai coauthor-guard` when the guard mode is warn or
  block
- `~/.claude-mem` — retired. dot writes nothing into it any more. The only
  operation on it is `dot ai memory retire-claude-mem --apply`, which moves
  the whole directory to the dated sibling `~/.claude-mem.archive-<YYYYMMDD>`
  (never a deletion). It is refused, before any step, while a claude-mem process
  runs, when the directory is a symlink, when any file it must read cannot be
  read, and under an explicit `--home`: the native `claude` and `codex` CLIs and
  launchd act on the invoking user's real state, so a scratch home can be
  planned and read back but never applied. `$CLAUDE_MEM_DATA_DIR` is honored
  for the person's own home only.
- `~/.claude-mem.archive-` — the dated archive that retirement creates; a name
  prefix, not a directory, because the date and a same-day counter follow it.
- `~/Library/LaunchAgents/com.dotfiles.claude-mem-bridge.plist` and
  `~/Library/LaunchAgents/com.dotfiles.claude-mem-sync.plist` — retired
  LaunchAgents. dot no longer writes either; `dot ai memory retire-claude-mem
  --apply` boots them out and removes the files. dot writes no LaunchAgent for
  amem, which has no resident process.
- `~/.claude/plugins/marketplaces/thedotmack` — the claude-mem marketplace
  checkout. dot no longer refreshes it. Retirement removes the marketplace
  and plugin through `claude plugin marketplace remove` and `claude plugin
  uninstall`, and the Codex ones through `codex plugin remove` and `codex
  plugin marketplace remove`, never by editing their files directly. Lines in
  `~/.claude/settings.json` that name claude-mem are printed for the person to
  remove; dot never writes that file.
- the Homebrew service `sh.brew.container`
  (`~/Library/LaunchAgents/sh.brew.container.plist`) — created by
  `brew services start container`, which `dot container setup` runs on macOS
  when brew reports the service unregistered; dot never writes the plist.
  On Linux, setup may run `apt-get install`/`pacman -S` for docker or podman,
  `systemctl enable --now docker` and `usermod -aG docker`, through sudo and
  only when no docker or podman is present and the user accepts (or passes
  `--yes`); an existing docker is never changed
- `~/Library/LaunchAgents/com.dotfiles.watchdog.reap.plist` — the user
  LaunchAgent that runs the watchdog reaper on its configured interval,
  written and loaded by `dot watchdog setup` and removed by
  `dot watchdog uninstall` (macOS only)
- `/Library/LaunchDaemons/com.dotfiles.watchdog.warp.plist` — the root
  LaunchDaemon that runs the WARP heal pass, installed with sudo by
  `dot watchdog setup` when `watchdog.warp` is enabled and removed by
  `dot watchdog uninstall` (macOS only). Trust note: the daemon executes
  the same user-installed `dot` binary (with `--home` pinned to the
  installing user). On the single-user Macs dot targets that user already
  holds sudo, so the user-writable binary is not a privilege boundary;
  hardening it would need a root-owned copy refreshed on every dot
  update, which is a documented non-goal for now. The kickstart target is
  re-validated against Cloudflare's WARP daemon label shape at run time,
  so a local edit of `warp.json` cannot redirect the root daemon at an
  arbitrary service
- `~/.config/monit/monitrc` — the monit control file rendered by
  `dot watchdog setup` when `watchdog.monit` is enabled (mode 0600, which
  monit requires of its control file), and removed by
  `dot watchdog uninstall` (macOS only)
- `~/Library/LaunchAgents/com.dotfiles.monit.plist` — the user LaunchAgent
  that runs monit in the foreground against that control file, written and
  loaded by `dot watchdog setup` and removed by `dot watchdog uninstall`
  (macOS only)
- `/Library/Application Support/dot/screensharing-heal` — the root-owned
  helper (mode 0755) that runs `launchctl kickstart -k
  system/com.apple.screensharing`; installed with sudo by
  `dot watchdog setup` when `watchdog.monit.screensharing` heals, exec'd by
  monit through passwordless sudo when the VNC handshake on 127.0.0.1:5900
  fails, and removed only by the interactive-only uninstall prompt. Root
  ownership is the security property: the monit agent runs as the user, so
  the file sudo executes must not be user-writable
- `/etc/sudoers.d/dot-watchdog-screensharing` — the sudoers drop-in (mode
  0440) granting the installing user passwordless exec of exactly that
  helper path and nothing else; validated with `visudo -c -f` before it
  goes live, and removed only by the interactive-only uninstall prompt
- `~/Library/Logs/dot/monit.log` — monit's own log, written by the monit
  agent (plus the unit's launchd stdout/stderr logs beside it)
- `~/.local/state/dot/watchdog/` — the watchdog machine state: the resolved
  config snapshot (`watchdog.yaml`) the scheduled reaper reads, the
  cross-run CPU history (`samples.json`), the WARP heal bookkeeping
  (`warp.json`: consecutive failures, reconnect attempts, last daemon
  restart, resolved WARP daemon label), and the pre-hardening power values
  (`power.json`) that uninstall restores from; written by
  `dot watchdog setup`, every `dot watchdog reap`/`dot watchdog warp`
  pass, and removed only by the interactive uninstall prompt
- `~/Library/LaunchAgents/com.dotfiles.beszel-agent.plist` — the user
  LaunchAgent that runs the Beszel external-monitoring agent, sourcing the
  secrets-managed env file before exec'ing the brew-installed agent binary;
  written and loaded by `dot watchdog setup` when `watchdog.beszel` is
  enabled, and removed (the plist only, never the env file) by
  `dot watchdog uninstall` (macOS only)
- `~/.config/beszel/agent.env` — the Beszel agent env file (hub
  HUB_URL/KEY/TOKEN/LISTEN), secrets-managed: an age-encrypted archive ↔
  plaintext pair registered in the `dot secrets` entry table, so
  init/backup/restore cover it like the SSH key and shell secrets. Watchdog
  setup only probes its presence — it never reads, renders, or removes the
  file — and the beszel plist sources it at agent start
- `~/.config/dot/telegram.env` — the Telegram alert channel env file
  (TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID), a secrets-managed archive ↔
  plaintext pair like the Beszel env. The watchdog notifier reads it lazily
  when `watchdog.notify.telegram.enabled` is on, so a rotated credential
  takes effect on the next scheduled run without re-running setup; a missing
  or incomplete file disables the channel silently
- `<workspace>/.dotfiles/peer/alert-state.json` — the peer sync alert
  episode position (failing since, last sent), written atomically by
  `dot peer sync` only when Telegram alerting is enabled and a transition or
  reminder actually sends, and removed when a recovery sends. `--dry-run`
  never writes it
- `<workspace>/.dotfiles/peer/log/sync.log` and `sync.log.1`..`.3` — the peer
  run log. A scheduled `dot peer sync` rotates it past 10 MB (renames, keeping
  three old files), then appends dated start and exit lines and the run's
  output; launchd's redirect of the job's output points at the same file
- `<workspace>/.dotfiles/peer/alert.lock/` — the PID lock serializing peer
  alert reporting across overlapping runs (the sync run lock is already
  released by the time the outcome is reported)
- `~/.local/state/dot/admission/` — the resource-admission controller state:
  the slot directories (`slots/`, one per repo plus the shared maintenance
  slot and a background slot per running `dot peer sync` or `dot sync push`,
  each holding a `lease.json` with the owner pid/start, heartbeat and
  deadline), the heavy-work pressure history (`history.json`), the verified
  native dot-update pressure and quiet-swap history (`history-prebuilt.json`),
  and the per-scope notify dedup marks (`notify/`). `history.json` is written
  by `dot admit` gate evaluations and by `dot ai run` and tooling updates
  through the same controller; slot acquire/heartbeat/release and defer
  notification also update this state. A completed `dot update` prebuilt-gate
  evaluation writes `history-prebuilt.json` atomically whether it admits or
  defers. This file is separate from `history.json`; prebuilt evaluations use
  the same host history lock, and admitted updates use the shared maintenance
  slot, without overwriting heavy history. `dot admit status` reads pressure
  history without changing it.
  The root is the real user's (`$HOME`), never a `--home` or CODEX_HOME
  override, so one repository has one slot per user. Slots are removed by
  their owner's release or by stale-owner recovery; both pressure-history
  files and notify marks are small JSON files with no scheduled cleanup
- `~/Library/Logs/dot/watchdog.log` — the watchdog JSON-lines event log
  (`dot watchdog log` tails it), plus the reaper unit's launchd
  stdout/stderr logs beside it
- the power-management keys `sleep` (charger profile), `autorestart`, and
  `womp` via `pmset`, and `restartfreeze` via `systemsetup` — written only
  by `dot watchdog setup --headless`, after the prior values are saved to
  `power.json`, and restored by `dot watchdog uninstall` under the
  interactive-only restore prompt. No other pmset/systemsetup key is
  touched.
- `~/.kimi-code/mcp.json` — only retirement writes it, removing the
  `claude-mem` entry; every other server entry is preserved. Its `amem`
  entry belongs to `amem setup kimi`, never to dot
- `~/.gemini/config/mcp_config.json` — the Antigravity CLI (agy) shared MCP
  config: only retirement writes it, removing the `claude-mem` entry; every
  other server entry is preserved. amem is set up per repository by
  `amem setup agy`, never in this global file
- `~/.qwen/settings.json` — only retirement writes it, removing the
  `claude-mem` entry; the `amem` entry belongs to `amem setup qwen`; every
  other key — Qwen's model, provider, auth, and UI settings — is
  preserved. Notably NOT dotfiles-managed: `~/.qwen/.env` (secrets),
  `~/.qwen/projects/**/chats/**`, `~/.qwen/memories/**`, and
  `~/.qwen/usage_record.jsonl` are machine state dot neither backs up
  nor restores
- `~/.kiro/settings/mcp.json` — the `amem` entry under
  `mcpServers` plus `"disabled": false`, written by `dot ai memory
  install`; retirement removes the `claude-mem` entry; every other
  server entry is preserved
- `~/.copilot/mcp-config.json` — the `amem` entry under
  `mcpServers` plus `"type": "local"` and `"tools": ["*"]`, written by
  `dot ai memory install`; retirement removes the `claude-mem` entry;
  every other server entry is preserved
- `~/.codex/config.toml` — the `tui.status_line` setting, patched by
  `dot ai hud apply`; the write is an atomic rename because Codex
  rewrites this file continuously. No other key is touched.
- `<workspace repo>/.git/index` and the repo's `HEAD` ref — moved
  together by `dot peer git realign --apply` through git's lockfile
  protocol (compare-and-swap `update-ref`, fast-forward only onto a
  strict descendant, the old value recorded in the reflog for undo;
  `--candidate-refs` only adds candidates, which must be strict
  descendants too).
  Only `--rescue` moves a branch sideways: HEAD's commits are first kept
  on a new `rescue/<yymmdd>-<branch>` ref (create-only; not while a
  branch named `rescue` blocks it; a pushed one also skips names the
  push remote's remote-tracking refs hold) and pushed where git pushes
  the branch (pushRemote, pushDefault, then its remote; only after
  `git ls-remote` shows the remote branches the rescue rests on
  unchanged since the last fetch, checked before anything is written;
  a branch mismatch's own upstream counts only when it visibly moved,
  not when it is deleted there, untracked here or unreadable)
  unless
  `--no-push` (a Git LFS repo, one the check cannot read, one whose
  push remote has a `rescue` branch, or one whose history has a commit
  with a rewritten twin in a remote branch's history, the history a
  rewrite took out, or where that cannot be checked, is rescued only
  with `--no-push`);
  then the branch, or for a branch mismatch the default
  branch (created with `branch.<name>.remote` and `.merge` pointing at
  origin's, or fast-forwarded; never while a linked worktree has it
  checked out, or is rebasing or bisecting it; HEAD re-pointed after
  re-checking it under the index lock), moves under the same protocol.
- `<workspace repo>/.gitmodules`: restored from the index by
  `realign --apply` only when it equals an older committed version or is
  missing (peer sync never carries it), then `git submodule sync` for the
  URLs it moves (`.git/config` and the submodule's default remote URL: its
  branch's remote, else origin), only in submodules the run names and that
  are not locked or mid-operation, each rewritten remote URL printed with
  its undo. With `--apply --fetch` a submodule whose gitlink commit is
  missing (and is a checkout of its own, never a directory git resolves to
  the parent) gets `git fetch --no-recurse-submodules` under git
  submodule's protocol rules (`GIT_PROTOCOL_FROM_USER=0`), after
  `remote set-url origin` when its URL moved (both URLs compared as git
  resolves them, `url.<base>.insteadOf` included); the old URL is put back
  if that fetch fails. Under the same flags a submodule that lacks a commit
  one of its parent's tied candidates records (and that the run does not
  leave alone) gets the same fetch from its current origin, once per run,
  before those candidates are compared.
  Every git command these runs start runs no hook of the repo it works
  in: `core.hooksPath=/dev/null`, `core.fsmonitor=false`,
  `hook.<event>.enabled=false` for the events they fire (git 2.55), and
  `hook.<name>.enabled=false` for each hook the repo's config defines
  (git 2.54) (a push to a local-path remote still runs that remote's
  receive hooks). Otherwise the worktree is never written by git; preview
  (`dot peer git status`, default `realign`, `--dry-run`) writes nothing;
  linked worktrees and locked, staged, conflicting or in-progress
  repositories are never touched.
- home paths the operator lists in the peer store's
  `home-paths-tracked.txt`, on both machines: regular files and symlinks
  (including dangling links and links to directories) may be created,
  replaced or removed by the baseline-aware tracked pass of `dot peer sync`.
  Link targets are fingerprinted as strings and are never followed; symlink
  ancestors observed by preflight are refused. This is the existing path-based
  transport, with the concurrent-directory mutation window documented in
  `docs/CEILINGS.md`. Simultaneous conflicting edits and
  accepted deletions preserve the receiving payload under
  `~/.dot-peer-conflicts/<timestamp>/from-peer/`. The peer store's
  `baseline-home.manifest`, `baseline-home.peer-target` and `tombstones.log`
  record the tracked transaction. Existing provenance, propagation policy,
  deletion caps and dry-run restrictions apply to regular files and links.
  This allowance requires explicit tracked-path selection; it does not grant
  skill installation or ownership of a link's target tree. The additive
  `home-paths.txt` list does not authorize deletion propagation.
  Explicitly selected tool skill roots may participate in this peer filesystem
  transport. This is a transport exception to the direct-write prohibition
  below, not generic skill deployment: sources, installer bundles, registry
  entries and per-target ownership remain managed by their existing owners.
- host files the operator lists under `host_merge` in the peer config (for
  example `~/.claude.json`), on both machines: before the additive
  host-path pass of a two-way `dot peer sync`, the entries of the listed
  top-level JSON keys from both copies are merged into the newer copy,
  which is written here (atomic rename, mode and owner kept, a symlink
  refused) and pushed to the peer with its mtime from a private copy. The
  additive pass of every run excludes the listed files; a create-only pass
  (`rsync --ignore-existing`, regular files only) copies one that exists on
  one machine only to the other and does not replace a copy there (see the
  host_merge ceiling for rsync's own window). A `--push-only` or `--pull-only`
  run merges nothing and holds a file on both machines. Other keys are the
  newer copy's; an unlisted file is only ever copied whole, as before.
  Never a file under a tool skill root (refused before a run starts), or
  one the additive host pass does not move.
- launchd jobs and apps named by the operator in the peer profile's
  `hooks` (`.dotfiles/peer/config.yaml`): the jobs of their existing
  `~/Library/LaunchAgents/<label>.plist` disabled and booted out, or
  re-enabled and bootstrapped, apps quit or opened, only as listed: by
  `dot peer setup` (on_activate, also on a refresh), `dot peer setup --off`
  (on_deactivate), a handover (on_deactivate here, last), a fence demotion
  (on_deactivate) and a peer `dot sync owner --set/--clear` that takes the
  coordinator role from this Mac (on_deactivate). The disable and enable
  write those labels' entries in launchd's per-user override database
  (`launchctl disable/enable gui/<uid>/<label>`), so a bootout survives a
  reboot; only labels dot disabled are enabled again. dot never writes
  those plists or the apps' files; an unset `hooks` touches nothing.
- `<workspace>/.dotfiles/peer/hooks-disabled.txt`: the launchd labels the
  role hooks disabled, one per line, so `on_activate` re-enables only those.

## dotfiles Must Not Write Directly

- anything under any tool skill root outside the explicit peer filesystem
  transport above (`~/.claude/skills/**`,
  `~/.codex/skills/**`, `~/.agents/skills/**`, `~/.gemini/skills/**`,
  `~/.gemini/antigravity/skills/**`, `~/.kimi-code/skills/**`,
  `~/.qwen/skills/**`, `~/.grok/skills/**`, `~/.config/opencode/skills/**`,
  and resolved alternate profile skill roots)
- skill source directories under `~/.maru/skills/**` or any configured
  `modules.ai.skills.ssot_path`
- `~/.maru/env/**`

Skill directories may be scanned for diagnostics. Backups/restores do not copy
skills. Dot may invoke Maru for selected installable registry entries or a
selected add-on's native installer for its own bundle; those tools retain write
ownership. A missing Maru capability defers sharing, never triggers direct
copies. Do not overwrite foreign skills, implicitly retarget links or enroll a
generic skill source in automatic updates.

## Maru Owns

- `~/.maru/**` except the two portable settings files above
- `~/.maru/skills/registry.json`
- `~/.maru/skills/<name>` runtime symlinks
- owned skill symlinks for selected Claude, Codex, Kimi, Qwen, Grok and
  OpenCode profiles; native/plugin/system entries remain excluded
- source reconciliation, registry validation, and duplicate-tier policy

If this boundary changes, update the matching Maru boundary document and the
workspace rule at `~/workspace/work/_meta/rules/skills-ssot.md`.
