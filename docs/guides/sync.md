# Sync and peer

## Sync secret policy

`dot sync` excludes credential-bearing paths by default, including `.ssh`,
`.gnupg`, common cloud and package-manager credentials, and private-key and
keystore extensions. An operator can explicitly re-include a path in
`allow.txt`; status and dry-run previews show every such sensitive override
without changing the requested transfer decision.

## Sync backups and conflicts

`dot sync push` keeps a copy under the mirror's `.sync-conflicts/<timestamp>/`
only when something could be lost: a propagated deletion, or an overwrite of a
mirror copy that changed after the last push. It also keeps one, to be safe,
when a file turns into a directory or link, or when it cannot prove the old
copy matched the last push. A routine update leaves no backup, and a mirror file the cloud rewrote with the same bytes and only a new
timestamp is not a conflict. `dot sync status` shows `Conflicts: N live` (the
conflicts the last push refused on) apart from `Backups: M directories
(size)`, and `dot sync status --json` carries `liveConflictCount` and
`backupBytes` next to `conflictCount` (retained backup directories). Old
backups stay until `dot sync conflicts prune`.

## Peer profile

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

A plan with more deletions than `max_delete` is refused with a digest of its exact deletion set. Review the set with `dot peer diff --list`, then run it once with `dot peer sync --approve-deletes <digest>`; a set that changed since refuses again with the new digest, and the config is never edited. Scheduled runs write dated start and exit lines to `.dotfiles/peer/log/sync.log`, which rotates at 10 MB and keeps three old files.

---

## Sync and peer operating model

`dot sync` sends a Git-aware union: root-repository tracked files plus approved untracked binary extensions, minus submodules, junk, and deny-by-default secrets. `allow.txt` records an explicit secret exception. A baseline-unknown mirror file stages under `inbox/gdrive/` for manual routing. The baseline also records every file a mirror push sent, even when rsync then stops with an error (a `--max-delete` stop comes after every transfer), so a file a live process removes while the push runs is a workspace deletion on the next push rather than a baseline-unknown mirror file. A push that leaves a planned deletion in the mirror fails. Local mirror pushes with delete propagation select rsync 3.x from PATH or the Homebrew fallback paths. Delete propagation refuses before any transfer when no modern client is available (`brew install rsync`); pushes without deletes can still use an older client. Status shows the selected binary path and version. A deletion can still remain after an I/O error or because a filter protects the name. `dot sync status` shows `Push stalled` when the last push run refused (a preflight block such as a missing mirror included, a pause excepted) or failed, or when the scheduler is on and no push completed within three intervals; a run that skipped files after an error (rsync exit 23) does not count as completed.

Run `dot sync names normalize --store=peer --dry-run` and then `--yes` for selected NFC-to-NFD names. It refuses invalid UTF-8, symlinks, and canonical collisions, moves deepest paths first, and rolls back completed moves on error. Names Dropbox and Windows cannot store (a segment ending in a space or period) are excluded from push plans and uploads; a mirror-only copy of such a name that the baseline proves the workspace put there (left by a rename) is a leftover, which a push with delete propagation moves into the workspace's `.sync-conflicts/<ts>/from-mirror/` when there are no more than `max_delete` of them (a budget apart from the push's own deletions). Any other mirror-only path with such a name may be a cloud file and is only listed. A push never sends symlinks, so a workspace file replaced by a symlink is treated like a deletion. With delete propagation on, the push moves the mirror copy into the same `from-mirror/` folder while the baseline proves it; an unproven copy is a conflict that only `--mode=force` moves. `dot sync names trim --dry-run` and then `--yes` renames the trailing-whitespace ones under the same preflight and rollback rules. Profiles use stores below `<workspace>/.dotfiles/<profile>/`; the peer profile shares the cloud workspace lock while other custom profiles do not.

`dot peer` is two-way peer sync, not the cloud mirror. Its deletion propagation quarantines baseline-recorded removals. Host paths split by list: entries in `home-paths-tracked.txt` get the same baseline-aware semantics (deletes propagate, simultaneous edits quarantine the losing payload under `~/.dot-peer-conflicts`), while entries left in `home-paths.txt` alone stay additive with newest-mtime wins — `.claude.json` included. `dot peer init` and `dot peer setup` seed the tracked list, with the Claude auto-memory entry included only when both machines share the workspace path. Keep the same dot version on both machines before scheduling because older peers do not understand tombstones. The coordinator records simultaneous conflicts in `peer-conflicts.log`; an offline peer exits zero for safe scheduled laptop runs.

With Telegram alerting on (above), `dot peer sync` reports its outcome through the watchdog notifier's transition/rate-limit alerter: a real transfer or safety-refusal error alerts critical, a two-way run that holds destructive transitions (baseline unchanged) warns, a complete run quarantining more deletions than `quarantine_threshold` warns, and the first complete run after an episode sends one recovery message. One message fires at the ok→fail transition, then at most one reminder per `reminder_interval` while the condition persists; the episode state lives in `<workspace>/.dotfiles/peer/alert-state.json`. The designed-clean shapes — an unreachable peer, a fence demotion, a lost scheduled lock race — never alert. `--dry-run` sends nothing and writes nothing. An alert delivery failure is a stderr warning and never changes the run's exit code, error, or baseline semantics.

Peer host paths exclude `known_hosts`, `~/.maru/env`, plugin caches, `~/.codex/auth.json`, and `~/.codex/config.toml`: they are trust, absolute-runtime, or keychain-coupled state rather than portable workspace settings.
