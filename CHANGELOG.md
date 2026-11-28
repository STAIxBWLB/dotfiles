# Changelog

Generated from `docs/releases/` by `make changelog`; do not edit by hand.

## v2.88.0

`dot ai family` now installs `arev`, the review CLI, with the other agent family tools, and `dot remote hold --dry-run` explains its GPU verdicts in full.

### New

- `dot ai family install --all` now includes `arev` (`@staix/agent-review`). Install it alone with `dot ai family install arev`; `dot ai family status` and `dot ai family rollback` handle it like the other tools. After install, dot prints `arev setup <host>`, which previews the setup; run it with `--apply` to register arev's MCP server and install its review skill. dot runs neither. (#479)

### Fixed

- `dot remote hold --dry-run --gpus N` only said how many GPUs were eligible. It now names the GPUs left out because another `dot` hold has them and suggests `dot remote status`, `--wait` or a lower `--gpu-min-free-mb`. (#480)
- `dot remote hold --dry-run` reported GPUs it "would lease" even when it could not read the lease store. It now says the leases could not be read and points to `dot admit status`, alongside any host-load verdict. (#480)
- `dot remote hold --gpus N` with N greater than the host's GPU count retried until `--wait` ran out. It now exits 75 at once, naming the device count. (#480)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.87.0...v2.88.0

## v2.87.0

Tools that start their own containers on a remote host, such as `ares`, can now hold a host slot and GPUs through `dot remote hold`, so they count toward the host's limits and show in status.

### New

- `dot remote hold -- COMMAND` takes one of the host's `max_runs` slots, checks the host load, runs COMMAND on this machine and frees the slot when it exits. A busy or overloaded host exits 75 without starting COMMAND, and `--dry-run` shows what it would take. (#473)
- `dot remote hold --gpus N --gpu-min-free-mb MB` also picks N GPUs on the host with enough free memory that no other `dot` hold from this machine is using, and passes their UUIDs to COMMAND in `DOT_REMOTE_GPUS`. Too few free GPUs exits 75. `ares` does not read `DOT_REMOTE_GPUS` yet. (#474)
- `dot remote status` and `dot admit status` list each hold with its program, pid, start time and GPUs. (#473, #474)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.86.1...v2.87.0

## v2.86.1

Help output now shows on/off flags as `--flag[=true|false]`, following an update of the flag library.

### Improved

- Every command's `--help`, and the generated command reference, now writes switch flags with their accepted values, for example `--dry-run[=true|false]` and `-h, --help[=true|false]`, instead of the bare `--dry-run`. How you pass them is unchanged: `--dry-run` still means true. (#469)

### Action needed

- If a script matches flag lines in `dot ... --help` output, update it to the `--flag[=true|false]` form after `dot update` or `brew upgrade dotfiles`. (#469)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.86.0...v2.86.1

## v2.86.0

When SSH to GitHub is blocked, git can now switch your GitHub remotes to HTTPS and switch them back once SSH works again.

### New

- `dot git transport` probes SSH and HTTPS to GitHub and points GitHub remotes at the one that works. In https mode, `git@github.com:` URLs go through your credential helper, such as `gh auth git-credential`. When neither transport works, the current mode is kept. Use `--check` to see the result without changing anything. (#468)
- `dot git transport setup` runs that check every 10 minutes on macOS, so a blocked GitHub SSH endpoint no longer stops fetch and push until someone notices. `dot git transport uninstall` removes it. (#468)

### Improved

- With `github_user` set, `~/.config/git/config` now includes `~/.config/git/github-transport.inc` instead of carrying a fixed `https://github.com/` → `git@github.com:` rewrite. `dot apply` writes the ssh mode there when the file is missing, so nothing changes until `dot git transport` picks https. (#468)

### Action needed

- Run `dot apply` after `dot update` so your git config includes the transport file. On macOS, run `dot git transport setup` if you want the automatic switch. (#468)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.85.1...v2.86.0

## v2.85.1

This release fixes `dot update` for installs that could not move to 2.85.0.

### Fixed

- `dot update` to 2.85.0 failed on standalone installs and on the verified Homebrew path with `release archive contains an unexpected or duplicate member "CHANGELOG.md"`; 2.85.1 archives carry only the files `dot update` accepts. (#465)

### Action needed

- If `dot update` failed with that message, run `dot update` again to install 2.85.1. Homebrew installs that already reached 2.85.0 are unaffected. (#465)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.85.0...v2.85.1

## v2.85.0

dot's help and messages now read the same in every command: grouped help with an example for each command, one form for warnings, and one form for dry-run previews.

### Improved

- `dot --help` and every subcommand's help group commands by purpose, wrap descriptions at the terminal width and use the same colors and markers as dot's reports. (#458)
- Help for every command is shorter and consistent: one-line descriptions, lowercase flag descriptions, `<placeholder>` usage lines such as `dot remote run [<path>] -- <command> [<arg>...]`, and examples on every command you can run. (#459, #460, #462, #463)
- Warnings from every command share one form: `dot: warning: <message>` on stderr, a warning marker on a terminal and, where dot knows one, the command that fixes it on the line below. This includes the `dot remote` "could not" diagnostics and the admission notes printed by `dot remote` and `dot admit`. (#451, #453, #454, #455, #461)
- Dry-run previews from `dot sync`, `dot peer`, `dot remote`, `dot backup`, `dot restore`, `dot watchdog`, `dot guard`, `dot tunnel`, `dot update`, `dot rollback`, `dot secrets`, `dot apps`, `dot noindex`, `dot ai` and `dot admit relax` print the pending marker followed by `would <action>`. (#453, #454, #455, #461)
- Progress rows from `dot apply`, `dot ws` and `dot sync push`, `fetch` and `pull` use the shared markers, so `--color` and `--ascii` now apply to them too. (#455)
- An unknown command such as `dot foo` prints the standard usage error followed by the `dot open foo` suggestion, instead of a longer custom message. (#451)
- `dot open --install-optional` reports a failed optional-tool install on stderr, followed by the full `brew install` command to retry it. (#451)

### Fixed

- A malformed amem memory marker found during `dot apply` showed on a success row; it now shows as a warning. (#461)

### Action needed

- After `dot update` or `brew upgrade dotfiles`, update scripts that match dot's human output. Warnings now start with `dot: warning:` (previously `warning:`, `Warning:`, `admission:` or `dot remote:`). Informational notes that started with `note:` now start with `dot: `, and the `Note:` and `Tip:` lead words are gone. Dry-run lines no longer carry `[dry-run]`, `dry-run:`, `--dry-run:` or `Would`; match `would <action>`, or use `--json` where the command offers it. `--json` output is unchanged. (#451, #453, #454, #455, #461)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.84.0...v2.85.0

## v2.84.0

dot's reports now share one look: color you can turn on or off, markers that read without color, a summary line at the end of every check, and exit code 2 when a command is typed wrong.

### New

- Every command takes `--color=auto|always|never`. Without the flag, dot follows `NO_COLOR`, `FORCE_COLOR` or `CLICOLOR_FORCE`, then whether stdout and stderr are each a terminal; `--json` output never carries color. The execution log lines dot used to print while it worked are hidden unless you pass `--verbose`. (#442)
- Reports use the terminal's own 16 colors, and each state has its own marker, so failed, missing, unknown and active rows read the same without color. Under a non-UTF-8 locale, or with `--ascii`, markers fall back to `ok`, `x`, `!`, `->` and `>`. `DOT_COLOR_THEME=tokyo-night-dark` or `tokyo-night-light` switches to a Tokyo Night palette. (#446)
- `dot status`, `dot diff`, `dot ws audit`, `dot layouts doctor`, `dot peer doctor`, `dot ai skills validate`, `dot ai memory audit` and `dot remote job check` end with one summary line counting failed, warning, ok and unknown results. (#447)

### Improved

- Report text wraps at the terminal width, or at `COLUMNS` when output is piped; check tables keep their name and state columns aligned and shorten long names with `...`, which `--verbose` prints in full. (#447)
- `dot peer doctor`, `dot ai memory audit`, `dot ai skills list`, `dot ai skills validate`, `dot ai skills status` and `dot remote job check` print their findings on stdout with the rest of the report, so piping the report keeps them. (#447)
- `dot remote verify` refuses to run as root on the host and checks that the image has a user matching your host account before it pushes anything; `--dirty` snapshots leave out the files `dot remote run` never sends, and `verify` and `src` clean up their own leftover containers instead of deferring. (#441)
- `dot remote job check` no longer creates the job directory on the host; it only checks that a job could create it. (#444)
- A failed `dot remote run` pass shows up to five lines of rsync's error instead of its last line, and suggests `--dry-run` to see what the pass would send. (#445)
- After `dot apply`, the global gitignore lets a repository commit `.agenthub/config.json`, `.agenthub/routing.toml` and `.agenthub/sdlc.toml`; the rest of `.agenthub/` stays ignored. (#436, #439)

### Fixed

- `dot --help --home <dir>` printed "unknown command" for the directory and failed; it now prints help. (#447)
- A `dot remote job run` whose command failed and whose `-o DIR` could not be created exited 1; it now exits with the command's own status and keeps the job directory. (#444)
- `dot remote run` left files at home when they sat under a directory whose name contains a backslash or a control character, and failed with rsync exit 23 on a non-git path where a directory had been replaced by a file. (#445)
- When `dot remote run` could not save its record of sent files, a file deleted before the next run was never removed from the mirror. (#445)
- Interrupting `dot remote verify` while it pushed exited 1; it now exits 128 plus the signal number. (#441)

### Action needed

- Scripts that read dot's errors must match the `dot: ` prefix instead of `Error: `. Unknown commands, unknown flags and invalid arguments now exit 2 instead of 1, and `dot layouts doctor` exits 1 when a required workspace tool is missing. (#447)
- `dot ai memory retire-claude-mem` and `dot ai vault retire-mcpvault` are removed, along with the `dot ai vault status` warning about the obsidian server. If a machine still has claude-mem or the obsidian MCP server registered, remove them with `claude plugin uninstall`, `claude mcp remove` or `codex mcp remove`. (#448)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.83.1...v2.84.0

## v2.83.1

`dot remote prune` no longer deletes refs outside the host's own folders through a symlink, and leaves another prefix's running verify alone.

### Fixed

- `dot remote prune --yes` could delete a verify ref in a repository outside `git_root` when the repository folder was a symlink or pointed elsewhere through a `.git` link, a `gitdir:` file or a `commondir` file, and could act outside the mirror root when `.dot-remote` was a symlink. Such a repository is now skipped, and a removal that meets one is refused with the cause and the next step. (#432)
- When two machines shared one remote account under different prefixes, `dot remote prune --yes` deleted the refs of a verify the other machine was still running. A ref is now kept while a verify container for that checkout runs, whatever its prefix; a verify another machine has pushed but not yet started is still not seen. (#432)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.83.0...v2.83.1

## v2.83.0

`dot remote` runs builds, tests and file-in, file-out jobs in a container on a host you can ssh to, with the laptop as the only place source is edited.

### New

- `dot remote run -- CMD` mirrors a checkout to the host with rsync and runs CMD there in a container. Only tracked files and untracked ones no ignore rule covers are sent, minus the built-in excludes, and nothing is copied back. It turns on once `~/.config/dotfiles/config.yaml` has a `remote:` block (see the servers guide), and `dot remote setup` checks the host and builds or pulls the image. A run holds one lease per checkout and one of the `max_runs` this machine may run on that host, both shown in `dot admit status` and `dot remote status`, and defers with exit 75 when a lease is held or the host's load is at its limit; `--wait` retries a held lease first, and `DOT_ADMISSION_IGNORE=remote-load` or `dot admit relax --ignore remote-load` steps past the host load check. Interrupting a run removes its container. (#423)
- `dot remote verify [PATH] [--dirty] [-- CMD]` runs a repo's gate on the exact commit, or with `--dirty` on the working tree as it is, in a fresh checkout on the host beside the base branch, without touching your index, HEAD or refs. `dot remote src [NAME] -- CMD` runs analysis of third-party code that is cloned only on the host. (#425)
- `dot remote job run -o DIR [-i FILE]... -- CMD` sends named input files up, runs CMD in its own job directory with the profile's CPU and memory limits, and brings `out/` back into DIR. Profiles live under `jobs:` of a remote host; when the host cannot take the job it runs locally under `dot admit` (`fallback: local`), and `dot remote job check` says whether the host can take one now. A failed job's directory is kept and its location printed. (#427)
- `dot remote prune` lists mirrors whose local checkout is gone, leftover verify refs and stopped containers, and removes them with `--yes`; anything in use on this machine or claimed on the host is skipped. (#429)
- `dot remote run -p PORT` forwards a port from the container to `127.0.0.1:PORT` on your machine, and `dot remote run --cgroup` gives the command a cgroup subtree of its own (`DOT_REMOTE_CGROUP`) for tools that set their own resource limits. (#429)

### Action needed

- The config file format moves to version 7 to hold the `remote` block. Update every machine that shares it with `dot update`; an older dot warns and refuses to save it. (#423)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.82.1...v2.83.0

## v2.82.1

`dot admit --ignore` now refuses an unknown rule name wherever it appears in the list.

### Fixed

- `dot admit --ignore all,slots` was accepted and skipped every rule, while `slots,all` was refused with "unknown ignore rule". An unknown name is now refused wherever it appears, in `--ignore`, `DOT_ADMISSION_IGNORE` and `dot admit relax --ignore` alike; `all` on its own or next to a rule name still skips all three rules. (#417)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.82.0...v2.82.1

## v2.82.0

You can now change the limits `dot admit` enforces, relax them for a while, or step past them for one run, and the defaults are unchanged.

### New

- An optional `admission` block in `~/.config/dotfiles/config.yaml` sets the limits behind `dot admit`, `dot ai update` and `dot update`: the host-pressure thresholds (`thresholds`), the verified `dot update` profile and its quiet window (`prebuilt`), how many heavy jobs one repo and the maintenance class may run at once (`slots`, 1 to 8), and the scan for heavy work that holds no slot (`uncovered`: `enabled`, `cpu_percent`, `ignore_names`). An unset key keeps today's value, and an invalid one stops the command with the key and the file named. (#413)
- `dot admit relax --for 1h --ignore pressure` or `--set slots.heavy_per_repo=2` changes the limits for up to 24 hours and then lifts itself; `dot admit relax --clear` ends it early. (#413)
- `dot admit --ignore pressure,slot,uncovered` (or `all`), or `DOT_ADMISSION_IGNORE=<same list>` for `dot ai update` and `dot update`, steps past those rules for one run. A skipped rule is still reported: the pressure reasons and the heavy jobs found are printed on stderr, and a job past the slot limit still holds a slot that `dot admit status` lists. Skipping `slot` admits up to 8 jobs past the limit, not an unlimited number. (#413)

### Improved

- `dot admit status` shows the value of every admission limit and where it came from (default, config or relax), an active relax and when it ends, and the number of each extra slot held past the first; `--json` carries the same limits and any config error whenever at least one `admission` setting, a relax or a config error is present. (#413)

### Action needed

- The config file format moves to version 6 to hold the `admission` block. Update every machine that shares it with `dot update`; an older dot warns and refuses to save it. (#413)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.81.0...v2.82.0

## v2.81.0

The command names kept for one release after v2.80.0 are gone, `dot sync` stops reading `mirror_path` and refuses to move a destination silently, and `dot apply` replaces a stale workspace link.

### Improved

- `dot sync status` warns when an installed scheduler still calls `dot sync --profile=<store>`, and `dot sync resume` rewrites that job to call `--store` and reloads it. (#407, #411)
- `dot sync push`, `pull` and `fetch` stop before copying anything when a store sets no `target` and an old `mirror_path` (in the store or in `~/.config/dotfiles/config.yaml`) names a different folder than the one sync would use. The error names both folders and the `dot sync target` command that picks one. (#409)
- `dot sync target` sets the folder for the current workspace only and no longer saves it for future workspaces: a new workspace uses the detected cloud folder until you run `dot sync target` there. With `--home`, it sets that user's workspace. (#409)

### Fixed

- `dot apply` failed with `file exists` when a workspace link pointed at a folder that had moved. It now replaces the stale link, and leaves a link in place with an error when it cannot tell whether its target exists. (#399)

### Action needed

- Upgrade both Macs of a `dot peer` pair with `dot update` (or `brew upgrade dotfiles`). `dot peer sync` between a Mac on v2.80.x and one on v2.81.0 fails when it normalizes file names on the other Mac. (#407)
- `dot check`, `dot doctor` and `dot preflight` are removed: use `dot diff --summary`, `dot layouts doctor` and `dot init --detect` (or `dot init --check-only`). (#400)
- `dot ai run` is removed: use `dot admit -- COMMAND [ARGS...]`. `dot admit` waits up to 30 minutes for a slot by default; add `--wait 6m` to keep the old wait. (#401)
- `dot ai list`, `dot ai status` and `dot ai rollback` are removed: use `dot ai tools list --details`, `dot ai backup status` and `dot ai family rollback`. `dot ai memory|vault|research install|status` are removed: use `dot ai mcp install|status amem`, `vmem` and `ares` respectively. (#405)
- `--include-secrets` is removed from `dot backup`, `dot restore` and `dot profile backup|restore`: use `--include-age-key`. (#402)
- `dot` no longer maps the old `anchor` names. `dot ai skills status` and `dot ai skills path` reject `--provider anchor` (use `maru`) and `--tool agents|gemini|antigravity|agy` (use `claude` or `codex`). `anchor` and `anchor-cli` given to `dot apps install` or listed in a custom profile now reach Homebrew unchanged and fail: use `maru-workspace` and `maru-cli`. Saved settings in `~/.config/dotfiles/config.yaml` are still converted when they load. (#404)
- `dot sync --profile` is removed: use `--store`. A scheduled sync installed for a store other than the default keeps failing until it is rewritten, because neither `dot update`, `brew upgrade dotfiles` nor `dot apply` rewrites an installed job. After upgrading, run `dot sync status --store=<store>` for each store you scheduled, and if it warns, run `dot sync resume --store=<store>` once. (#407, #411)
- `mirror_path` is no longer read. If `dot sync` refuses because the destination would change, run `dot sync target <folder>` with the folder you want; `dot sync push --dry-run --store=<store>` shows whether a store is affected. The key is dropped on the next save, with a note, once the store sets `target` or the value names the folder sync already uses; until then it is kept so the refusal keeps working. (#409)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.80.1...v2.81.0

## v2.80.1

`dot apply` keeps your own shell and ssh lines, stops restarting a container system you stopped, and lets you turn conda off.

### New

- `~/.zshrc.local` and `~/.bashrc.local` are sourced last by the managed `~/.zshrc` and `~/.bashrc`. Lines you or an installer add there survive `dot apply`. (#397)
- `modules.conda.enabled: false` in `~/.config/dotfiles/config.yaml` turns the conda module off on that machine. (#397)

### Fixed

- Hosts defined under `~/.ssh/config.d/` resolved only when connecting to github.com after `dot apply`, because the rendered `~/.ssh/config` included them inside the github.com block. The include is now the first line, so it applies to every host. (#397)
- `dot apply` started a container system you had stopped and re-registered its login job. It now leaves the service alone; `dot container setup` starts it. (#397)

### Action needed

- Before running `dot apply`, move lines that installers appended to `~/.zshrc` and `~/.bashrc` into `~/.zshrc.local` and `~/.bashrc.local`, and move extra `Include` lines from `~/.ssh/config` into a file under `~/.ssh/config.d/`. `dot apply` rewrites those three files and keeps a backup of each. (#397)
- The config file format moves to version 5. Update every machine that shares it with `dot update`; an older dot warns and refuses to save it. (#397)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.80.0...v2.80.1

## v2.80.0

Audit the agents' own memory, a simpler command set with old names kept for one release, and the README split into topic guides.

### New

- `dot ai memory audit` reads each agent's own memory (Claude Code, Codex, Qwen, Grok, jcode). Per store, it reports the size of the always-loaded index against its limit, index links to missing files, files no index names, and lines that use a retired term listed in `~/.config/dotfiles/agents/memory-markers.yaml`. It writes nothing, and exits 1 when an index is over its limit or links a missing file. `dot ai memory archive <agent> <path>...` moves stale files out of the loaded layer and prints the command that moves them back. (#368)
- `dot ai mcp install` and `dot ai mcp status`, with `amem`, `vmem` or `ares`, register and check the family tools' MCP servers in one place. (#391)
- `dot init --check-only` scans the environment, and `dot init --detect` writes a config from the detected system without prompts. `dot init --profile <p>` now sets the profile. (#389, #388)
- `dot diff --summary` lists each module's pending changes without the commands. (#389)

### Improved

- `dot admit` limits Cargo, Rust tests and Go to two workers by default (`--no-limit-threads` keeps your settings), and takes `--project`. (#385)
- `dot` with no arguments and `dot usecase` show the current commands, now including peer sync, the AI agents and `dot ws`. A test keeps them in step with the real command set. (#382)
- `dot sync` names its store with `--store` (`--store=peer` for the peer store). `--profile` and `--module` appear only on the commands that read them. (#388)
- The one-domain backup and restore commands say that `dot backup` and `dot restore` are the front door. (#390)
- Old settings such as the `anchor` skills provider, retired skills tools and the `anchor` cask are rewritten to their current names when the state file loads, and saved that way next time. (#387)
- A machine whose peer still runs a dot older than 2.70.23 is told to update it, instead of syncing without the owner fence. (#384)
- The README is down to install, setup and the module table. Everything else lives in topic guides under `docs/guides/`. (#392)

### Fixed

- `dot ws mkdir`, `mv` and `rm` with an `ssh:` sync target changed a default local folder instead of the mirror. They now refuse the mirror half and name `dot sync push`. (#381)
- A workspace still on the pre-2.59 sync store (`.dotfiles/gdrive-sync/`) is refused with the exact `mv` that moves it, instead of risking a sync from an empty baseline. (#383)

### Action needed

- Renamed commands keep working for one release and print the new name on stderr; update scripts and notes. `dot check` is now `dot diff --summary`, `dot doctor` is `dot layouts doctor`, `dot preflight` is `dot init --detect` and `dot preflight --check-only` is `dot init --check-only`. (#389)
- `dot ai run` is now `dot admit`. (#385)
- `dot ai rollback` is now `dot ai family rollback`, `dot ai status` is `dot ai backup status`, `dot ai list` is `dot ai tools list --details`, and `dot ai memory|vault|research install|status` are `dot ai mcp install|status amem|vmem|ares`. (#391)
- `--include-secrets` is now `--include-age-key` on `dot backup`, `dot restore` and `dot profile backup|restore`. (#390)
- `dot sync --profile` is now `dot sync --store`. (#388)
- Removed with no alias: `dot gsync`, `dot gdrive-sync`, `dot sync mirror` (use `dot sync target`) and `dot sync sync` (use `dot sync push`). If this machine was last set up before 2.59, run `dot sync setup` once so the old scheduler unit is removed. (#383)
- `--profile` and `--module` are no longer accepted by every command. `--profile` works on `apply`, `diff`, `config`, `status`, `init`, `watchdog setup|notify` and `container setup`; `--module` works on `apply` and `diff`. (#388)
- `dot apply` no longer removes files retired before 2.65. If any are still there, you may delete them by hand: `~/.config/shell/30-ai-tools.sh`, the pnpm keys in `~/.npmrc`, `~/Library/LaunchAgents/com.rclone.workspace-bisync.plist`, `~/Library/LaunchAgents/com.entelecheia.spotlight-nmi.plist`, `~/.local/bin/nmi-stamp`, `~/.config/shrc/40-spotlight-nmi`, and a dot-managed `~/.config/github-copilot/AGENTS.md`. (#386)
- Update both Macs to this release with `dot update`. A peer on a dot older than 2.70.23 is now refused. (#384)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.79.0...v2.80.0

## v2.79.0

Open the git and file views of an ahub workspace with one command or a tmux menu, and the scheduled peer sync no longer stops on a symlink with a Korean name.

### New

- `dot hub <project> git` and `dot hub <project> files` focus the workspace's `git` or `files` window, or add one running lazygit or yazi (with fallbacks when those are missing). They only add or focus a window and never type into a pane you are using. (#363)
- With `modules.workspace.ahub_keys: true` in your config, `dot apply` adds a `C-a A` menu to tmux with Hub console, Git, Files and Status for the current workspace. It is off by default and adds lines without changing existing bindings. (#363)

### Fixed

- `dot hub` run from a tmux key binding acted on the most recently used session instead of the one you pressed the key in. (#363)
- `dot peer sync` and `dot peer diff` stopped with `malformed non-regular warning` when the workspace held a symlink or other special file with a Korean name, so the scheduled peer sync kept failing after 2.78.0. (#365)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.78.0...v2.79.0

## v2.78.0

Install and update every agent-hub family tool with one command, bring git state up to date on the Mac that did not push, and peer sync works again with Homebrew rsync 3.5.

### New

- `dot ai family install --all` installs ahub, amem, vmem, acon and ares in one run, and `dot ai family install` now takes several tool names. (#357)
- `dot ai family update` moves every installed family tool to its latest release; name tools to update only those. `dot ai family update --dry-run` shows which tools have a newer release without changing anything. (#357)
- `dot peer git fetch` fetches every workspace repo from its upstream, then shows where each repo stands, so the Mac that did not push sees the commits the other Mac pushed. `dot peer git realign --apply --fetch` fetches the same way first, so one command moves HEAD and index up to date. Before, such a repo was reported `at-tip` against a stale `origin/main`. (#361)

### Improved

- One family tool failing no longer stops the others; the command finishes the rest and exits with an error naming each tool that failed. (#357)
- `dot ai family update` never moves a tool to a version older than the one it runs, for example after `--version` installed a release newer than the registry's latest. (#357)

### Fixed

- `dot ai family install acon` said acon had no published package and installed nothing. It now installs `@staix/agent-console`. (#357)
- With Homebrew rsync 3.5, `dot peer sync` and `dot peer diff` stopped with `peer inventory: rsync printed the name ... with \#ooo octal escapes` whenever the workspace held a file with a Korean name, so the scheduled peer sync failed on every run. Such names are read correctly again. (#359)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.77.1...v2.78.0

## v2.77.1

Fixes `dot` hanging at start on a Mac where a dot background job ran while Homebrew installed the new cask.

### Fixed

- After the move to the Homebrew cask in 2.77.0, `dot` could hang without output, and `dot update` stopped with `signal: killed`, when a dot LaunchAgent started while Homebrew was installing the cask. The cask now clears the download's quarantine before it links `dot`. (#355)

### Action needed

- If `dot` hangs after 2.77.0, run `brew upgrade --cask staixbwlb/cask/dotfiles`, then reload each dot job in `~/Library/LaunchAgents` so it starts the new binary: `launchctl bootout gui/$(id -u)/com.dotfiles.<job>` and `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.dotfiles.<job>.plist` (for example `sync`, `peer`, `noindex`). (#355)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.77.0...v2.77.1

## v2.77.0

Homebrew now installs dot as the `dotfiles` cask instead of a formula, and an existing formula install moves to the cask on its next update.

### Improved

- `brew install staixbwlb/cask/dotfiles` installs the `dotfiles` cask, with the same `dot` and `dotfiles` commands, on macOS and Linuxbrew. The tap now audits every dot release before it reaches you. (#351)

### Action needed

- On a Homebrew install, run `dot update`. Homebrew moves the formula install to the cask while it updates, when the `staixbwlb/cask` tap is trusted. If it cannot, dot prints the two commands to run: `brew uninstall --formula --force dotfiles && brew install --cask staixbwlb/cask/dotfiles`. (#351)
- After the move, run `dot guard enable`, `dot noindex setup` and `dot apply` where you use them, so nothing still runs the old formula's binary, then remove it with `brew uninstall --formula --force dotfiles`. (#351)
- From 2.75.0 or older, `dot update` can stop with "the tap still offers" after Homebrew has already moved the install. Run `dot update` once more; it then reloads the dot LaunchAgents. (#351)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.76.0...v2.77.0

## v2.76.0

dot now registers the ares research tool, opens agent-hub-first tmux workspaces, and can update a Homebrew cask install ahead of the move from formula to cask.

### New

- `dot open <project> --layout ahub` opens the agent-hub console beside two peer slots and a `verify` window, sized to your terminal; below 200x56 it becomes `ahub-compact`, one full-size window per slot. The console opens only when the project's hub runs, and the hub or a peer starts only when you press Enter in its slot. (#350)
- `dot hub <project> [console|status|ui]` opens the project's hub console, status or dashboard. Inside the project's workspace it jumps to the AHUB pane, or adds one `hub` window to an older session; it never starts the hub or rearranges panes. (#350)
- `dot ai research install` and `dot ai research status` register ares (`@staix/agent-research`) for Kiro and Copilot, add a short research block to your agents file, and print the `ares setup` lines for the other agents. Only ares tools its manifest marks read-only are auto-approved; tools that reach the network, run containers or change a study keep their approval prompt. `dot ai family install ares` installs it. (#349)

### Improved

- `dot open --layout <name>` on a session that is already running now says the existing layout was kept, instead of attaching silently. (#350)
- `dot update` now also upgrades a Homebrew cask install of dot, and follows Homebrew when it moves a formula install to the cask. Nothing changes for current formula installs in this release. (#352)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.75.0...v2.76.0

## v2.75.0

`dot admit` and the commands it gates no longer defer behind a remote verify wrapper, and release archives now carry signed build provenance.

### New

- Every release archive listed in `checksums.txt` has a GitHub build provenance attestation; check a download with `gh attestation verify <archive> -R STAIxBWLB/dotfiles`. (#343)

### Improved

- New releases reach the Homebrew tap only after the tap's audit passes, a few minutes after GitHub. In that window `dot update` on a Homebrew install says the release is out but the tap does not offer it yet, and asks you to run it again shortly. (#343)

### Fixed

- `dot ai family install`, `dot update` and other gated work deferred with `uncovered heavyweight work: python(...)` while a `remote_dev.py verify ... -- bun install` wrapper waited on a remote host, although nothing heavy ran on the Mac. A node or python wrapper's forwarded command after `--` no longer counts as local work. (#342)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.74.0...v2.75.0

## v2.74.0

`dot ai family install ahub` now installs and versions the agent-hub CLI, and `ahub` works right after `bun add -g`.

### New

- `dot ai family install ahub [--version X.Y.Z]` installs the global agent-hub CLI side by side like amem and vmem, links `~/.local/bin/ahub`, and `dot ai rollback ahub` switches back. Afterwards it prints the `ahub setup` command that moves the shared Claude plugin to the same version; dot does not run it. Project daemons that `ahub upgrade` runs are untouched. (#336)

### Fixed

- `bun add -g @staix/agent-hub && ahub setup` ended in `zsh: command not found: ahub` because the bun global bin directory was not on PATH. New shells now add `~/.bun/bin` after the dot-managed directories, so older bun-global copies of amem or vmem never take their place. (#334)

### Action needed

- Run `dot apply` after upgrading, then open a new shell, to pick up the PATH change. (#334)
- To let dot manage ahub, run `dot ai family install ahub`, then the `ahub setup` line it prints, and restart Claude sessions. (#336)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.73.1...v2.74.0

## v2.73.1

`dot admit` no longer stays stuck in "recovering" after a resource-pressure episode ends.

### Fixed

- After memory, thermal or CPU pressure cleared, `dot admit` kept deferring with "recovering from resource pressure (5m0s of normal telemetry still required)" for hours, even on an idle machine. It now keeps checking through that five-minute countdown within `--wait` and runs the command once recovery completes. Real pressure still defers at once, and `--wait 0` still checks only once. (#333)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.73.0...v2.73.1

## v2.73.0

`dot update` can now install an exact release, including an older one, and `dot rollback` switches back to the previous `dot` without downloading.

### New

- `dot update --version X.Y.Z` installs that exact stable release, including a downgrade, with the same checksum and runtime checks as a normal update. Standalone installs keep each version under `~/.local/share/dotfiles/tools/dot/<version>/` and point `~/.local/bin/dot` at the current one, so a session already running `dot` keeps its own binary. The first update after this release saves your existing `dot` as the previous version. (#329)
- `dot rollback` makes the previous `dot` current again, without a download. Running it again switches back. After rolling back to a release older than this one, which has no `rollback` command, run the saved newer binary directly: `~/.local/share/dotfiles/tools/dot/<version>/bin/dot rollback`. (#329)

### Improved

- `dot update` keeps the current and previous `dot` and any version a running process still uses, and removes older ones. When it cannot tell which version a process uses, it keeps them all and says so. (#329)
- Guard hooks registered by `dot guard enable` now point at the active install (the `current` link, or Homebrew's `opt` path), so they keep working across `dot update` and `dot rollback` without being rewritten. (#329)
- On a Homebrew install, `dot update --version` and `dot rollback` stop with the `brew` commands to use instead, and never touch Homebrew's files. (#329)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.72.1...v2.73.0

## v2.72.1

`dot ai family status` no longer reports a working `~/.local/bin` link as broken.

### Fixed

- `dot ai family status` said `~/.local/bin/amem does not point at current` (likewise for vmem) when that link was relative, such as `../share/dotfiles/tools/amem/current/bin/amem`, even though it pointed at the current version. `dot ai family install` also treated such a link as another install and asked for `--adopt`. Both now accept it. (#325)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.72.0...v2.72.1

## v2.72.0

`dot` can now install amem and vmem side by side by version and roll them back, and it accepts newer amem, vmem, Claude Code and Codex releases inside their supported ranges instead of one exact version.

### New

- `dot ai family install <id> [--version X.Y.Z]` installs amem or vmem into its own version folder under `~/.local/share/dotfiles/tools/` and points `~/.local/bin/<bin>` at the current one, so sessions running the old version keep working. `dot ai family status` shows the current and previous versions, and `dot ai rollback <id>` switches back to the previous one. ahub is left to `ahub upgrade`. It needs npm, runs only when you ask, and leaves an existing `~/.local/bin` link from another install alone unless you pass `--adopt`. (#321)

### Improved

- `dot ai memory status` and `dot ai vault status` accept any amem from 0.14.0 and any vmem from 0.2.1 below 1.0.0, so a newer release such as amem 0.17.0 no longer shows the "not the pinned version" warning. (#320)
- The amem and vmem tools allowed by `dot`'s agent permission policy now include the newer tools (`amem_evidence`, `amem_brief`, `amem_correct`, `vmem_skills_mine`), and follow the tool list a release publishes. amem tools that delete, forget, move or purge are never granted. (#320)
- The adaptive permission policy works with any Claude Code from 2.1.283 below 3.0.0 and any Codex from 0.157.1 below 1.0.0, instead of one exact version each. (#320)
- `dot ai update`, `dot apply` and the scheduled update now list the running processes of a tool before replacing it. Nothing is stopped; the list tells you which sessions to restart. (#320)
- The Vault section that `dot ai vault install` writes into your agent instructions now tells agents to find notes with `vmem_search` first and lets them read notes as files. Writes still go only through vmem. (#322)

### Action needed

- After `dot update`, run `dot ai vault install` and then `dot ai agents apply` to pick up the new Vault instructions, and restart agent sessions. (#322)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.71.5...v2.72.0

## v2.71.5

`dot ai memory status` now expects amem 0.14.0, the version agent-hub now pins.

### Improved

- `dot ai memory status` and the install hints name `@staix/agent-mem` 0.14.0. With 0.14.0 installed, the "not the pinned version" warning no longer appears. (#317)

### Action needed

- Upgrade amem with `bun add -g @staix/agent-mem@0.14.0`, then run `dot update`. Restart Claude and Codex sessions so they load the new amem. Do not run `amem migrate --public-prune-view` until every hub and amem sharing the memory home runs 0.14.0: older readers refuse an activated home. (#317)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.71.4...v2.71.5

## v2.71.4

`dot ai memory status` now expects amem 0.13.0, the version agent-hub 0.23.0 needs, and no longer tells you to downgrade to 0.11.0.

### Improved

- `dot ai memory status` and the install hints name `@staix/agent-mem` 0.13.0. With 0.13.0 installed, the "not the pinned version" warning no longer appears. (#315)

### Action needed

- Upgrade amem with `bun add -g @staix/agent-mem@0.13.0`, then run `dot update`. Restart Claude and Codex sessions so they load the new amem. (#315)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.71.3...v2.71.4

## v2.71.3

`dot ai vault status` now expects vmem 0.2.1, which keeps `vmem tidy` and `vmem promote` from writing into a vault that was re-registered mid-run.

### Improved

- `dot ai vault status` and the install hints name `@staix/vault-mem` 0.2.1. With 0.2.1 installed, the "not the pinned version" warning no longer appears. (#314)

### Action needed

- Upgrade vmem with `bun add -g @staix/vault-mem@0.2.1`, then run `dot update`. Restart Claude and Codex sessions so they load the new vmem. (#314)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.71.2...v2.71.3

## v2.71.2

`dot ai vault status` now expects vmem 0.2.0, which lets each vault set how strictly email, phone numbers and custom patterns are screened.

### Improved

- `dot ai vault status` and the install hints name `@staix/vault-mem` 0.2.0. With 0.2.0 installed, the "not the pinned version" warning no longer appears. vmem 0.2.0 adds `vmem vaults pii <vault>` to view or change PII screening per category and per surface (notes, log, promotion). (#312)

### Action needed

- Upgrade vmem with `bun add -g @staix/vault-mem@0.2.0`, then run `dot update`. Restart Claude and Codex sessions so they load the new vmem. (#312)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.71.1...v2.71.2

## v2.71.1

`dot ai vault status` now expects vmem 0.1.2, which can rename notes whose file names break the naming rule.

### Improved

- `dot ai vault status` and the install hints name `@staix/vault-mem` 0.1.2. With 0.1.2 installed, the "not the pinned version" warning no longer appears. (#310)

### Action needed

- Upgrade vmem with `bun add -g @staix/vault-mem@0.1.2`, then run `dot update`. Restart Claude and Codex sessions so they load the new vmem. (#310)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.71.0...v2.71.1

## v2.71.0

`dot ai vault` registers the vmem vault server and retires the Obsidian MCP server, and agent sessions now grant vault tools on vmem instead of obsidian.

### New

- `dot ai vault install` registers `vmem mcp` for Kiro and Copilot, adds the vault block to your agent instructions, and prints the `vmem setup <host> --apply` line for Claude, Codex, Kimi, Qwen, pi, jcode and Antigravity. `--dry-run` shows the changes without writing. (#307)
- `dot ai vault status` shows the vmem version against the pin (0.1.1), the Kiro and Copilot entries, the instruction block, and warns while an `obsidian` MCP server is still registered. (#307)
- `dot ai vault retire-mcpvault` removes the `obsidian` server from the files dot owns and prints the commands to run for Claude and Codex. Dry run unless you pass `--apply`; it refuses while `vmem` is not on PATH, and keeps `obsidian` in any config that has no working vmem entry yet or whose agent is not in your selection. (#307)

### Improved

- Session knowledge approvals grant the vault tools on the `vmem` server; an `obsidian` binding no longer earns a vault grant. (#307)

### Action needed

- Install vmem first (`bun add -g @staix/vault-mem@0.1.1`), run `dot update`, then `dot ai vault install`, `dot ai agents apply` and `dot ai vault retire-mcpvault` (dry run, then `--apply`). Restart the Claude and Codex sessions so the new MCP registration loads. (#307)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.52...v2.71.0

## v2.70.52

`dot ai memory retire-claude-mem` now recognizes the import done by amem 0.11.0 and works on the Claude profile you actually use.

### Fixed

- `dot ai memory retire-claude-mem --apply --import-complete` refused with "no amem import cursor with a handled row" even after `amem import claude-mem` succeeded, because it did not look where amem 0.11.0 keeps the work-list and capture progress. It now finds them; the other safety checks are unchanged. (#304)
- With `CLAUDE_CONFIG_DIR` set (for example an Orca profile), `dot ai memory retire-claude-mem` checked `~/.claude` while the `claude` commands it runs changed the active profile. It now reads and reports the active profile. The dry run names that profile, lists the files to back up before `--apply`, and shows claude-mem left in the default `~/.claude` separately without changing it. (#304)

### Action needed

- Run `dot update`, then `dot ai memory retire-claude-mem` for a dry run. Back up the files it lists, quit the Claude and Codex sessions that still load claude-mem, then run it with `--apply --import-complete`. To retire the default `~/.claude` profile as well, run it again with `env -u CLAUDE_CONFIG_DIR`. (#304)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.51...v2.70.52

## v2.70.51

dot now expects amem 0.11.0 and leaves the clients amem sets up itself to `amem setup`.

### Improved

- `dot ai memory status` expects `@staix/agent-mem` 0.11.0 and no longer tells you to install 0.6.0, which would have downgraded amem below what agent-hub needs. (#301)
- `dot ai memory install` now writes the amem MCP entry only for Kiro and Copilot, and prints the `amem setup` lines for Claude Code, Codex, Kimi, Qwen, pi and Antigravity. It no longer overwrites what `amem setup` wrote for Kimi and Qwen, and no longer adds a global Antigravity entry. `dot ai memory status` lists those three as managed by `amem setup`. (#301)

### Action needed

- After `dot update`, install amem 0.11.0 exactly, then run `amem setup all --apply` in your terminal, and `amem setup agy --apply` inside each repository that uses Antigravity. For Claude Code, use either `amem setup claude` or the agent-mem plugin, not both. (#301)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.50...v2.70.51

## v2.70.50

amem replaces claude-mem as your agents' memory, and a workspace file replaced by a symlink no longer stalls `dot sync push`.

### New
- `dot ai memory retire-claude-mem` lists everything claude-mem left on the machine. With `--apply --import-complete` it removes the claude-mem plugins, MCP entries and LaunchAgents, and moves `~/.claude-mem` to `~/.claude-mem.archive-<date>` instead of deleting it. Without `--apply` it changes nothing. (#296)

### Improved
- `dot ai memory install` registers amem (`amem mcp`) for Kimi, Qwen, Antigravity, Kiro and Copilot, and prints the lines that add it to Claude Code and Codex. `dot ai memory status` compares the installed amem with the version dot expects (0.6.0). `dot ai agents apply` writes an amem block in place of the claude-mem block. (#296)

### Fixed
- After a workspace file was replaced by a symlink, every `dot sync push` refused with "local has non-file entry: symlink", and `--mode=force` left the old copy in the mirror. If the mirror copy is the one dot last pushed, the push now moves it into the workspace's `.sync-conflicts/<ts>/from-mirror/`. Any other copy stays a conflict, and `--mode=force` now moves it too. (#299)
- `dot` is built with Go 1.26.10, which fixes the standard-library vulnerability GO-2026-6809. (#297)

### Action needed
- `dot ai memory sync`, `update`, `bridge` and `mcp-server` are gone. After `dot update` (or `brew upgrade dotfiles`), install amem 0.6.0 or later, run `dot ai memory install`, and add the printed lines for Claude Code and Codex. (#296)
- Once `amem import claude-mem` has finished, run `dot ai memory retire-claude-mem` to review the list, then again with `--apply --import-complete`. Remove the `~/.claude/settings.json` lines it prints. (#296)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.49...v2.70.50

## v2.70.49

`dot admit` no longer defers your builds and tests because of a busy app or background tool that is not working in any repository.

### Fixed
- `dot admit` deferred heavy work with "uncovered heavyweight work ... repo=" whenever a busy desktop app, a system service or a background tool (for example a menu-bar app's own commands, or claude-mem's observer) was running outside every repository. Such a process now counts only as host load. Builds, tests and installs outside a repository, and processes whose working directory cannot be read, still defer as before. (#295)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.48...v2.70.49

## v2.70.48

The global git ignore file now covers jcode's working directory.

### Improved
- The global git ignore file written by `dot apply` ignores `.jcode/`, alongside the other AI agent tool directories. (#293)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.47...v2.70.48

## v2.70.47

The hourly claude-mem peer sync no longer runs for hours or blocks heavy work on the Mac.

### Fixed
- The scheduled `dot ai memory sync` could run for hours at full CPU while holding the claude-mem database, so peer sync stopped, imports on the other Mac failed with "database is locked (SQLITE_BUSY)", and `dot admit` deferred every heavy job until the process was killed. Importing a week of overlap now takes seconds instead of hours. (#290)
- `dot ai memory sync` now stops on SIGTERM, and a new `--timeout` flag (default 10 minutes) ends a run that takes longer. In both cases the unfinished import is rolled back. The log names each step as it starts and ends, so a failed run shows where it stopped. (#290)
- The scheduled sync agent stops a run that is still going after 15 minutes, so a stuck run no longer holds the hourly slot. (#290)

### Action needed
- Run `dot update` (or `brew upgrade dotfiles`) on both Macs: each Mac's `dot` imports what the other one sends. (#290)
- Then run `dot ai memory install --peer <target>` once to rewrite the sync agent with the 15-minute limit. (#290)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.46...v2.70.47

## v2.70.46

`dot ai memory install` now finishes on Macs where the Antigravity CLI left an empty MCP config file.

### Fixed
- `dot ai memory install` no longer stops with "parse MCP config ~/.gemini/config/mcp_config.json: unexpected end of JSON input" when agy created that file empty; it adds the claude-mem server to it. (#287)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.45...v2.70.46

## v2.70.45

Grok and the Antigravity CLI (`agy`) now get the same workspace context and memory recall as your other agents.

### New
- `dot ai memory install` adds the claude-mem MCP server to agy's `~/.gemini/config/mcp_config.json`, keeping your other servers, and `dot ai memory status` shows it. (#284)

### Improved
- `dot ai agents status` says Grok reads your agents file through `~/.claude/CLAUDE.md`. dot no longer writes `~/.grok/AGENTS.md`, which Grok never read, and `dot apply` removes a copy dot wrote earlier. (#284)
- `dot peer init` help suggests an SSH alias for the other Mac over WARP instead of Tailscale MagicDNS. (#281)

### Fixed
- Scheduled `dot ai memory sync` runs no longer stop with "importing sdk_sessions: database is locked" when claude-mem is recording on the receiving Mac; the import now waits for the worker's write to finish. Upgrade both Macs, since the import runs on the receiving side. (#283)
- Grok no longer loads an outdated copy of your agents file: `dot apply` removes the old dot-written `~/.cursor/AGENTS.md`. (#284)

### Action needed
- After `dot update`, run `dot ai agents apply --tool antigravity` once to create `~/.gemini/GEMINI.md`, then `dot apply` and `dot ai memory install`. (#284)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.44...v2.70.45

## v2.70.44

`dot peer sync` no longer stops on an unreadable folder the run excludes when it checks file names on the other Mac.

### Fixed
- On an NFD-migrated workspace, `dot peer sync` no longer fails with "permission denied" on the other Mac for a folder in a linked worktree or in your peer `ignore.txt`, including a line you just added that has not reached the other Mac yet. An unreadable folder that does sync still stops the run. (#280)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.43...v2.70.44

## v2.70.43

Peer and mirror syncs stop leaving routine backups and holding up other work, and `dot update` can wait for its slot and says why it takes the slower path.

### New
- `dot peer sync --approve-deletes <digest>` runs one reviewed set of deletions over `max_delete` without editing the config. A refused run prints the digest; review the set with `dot peer diff --list`. If the set changed since, the run refuses again with the new digest. (#275)
- `dot update --wait 30m` waits for a busy maintenance slot instead of stopping at once with "retry later". (#276)

### Improved
- `dot update --check` says which path an update would take and, on a Homebrew install, which check keeps it off the faster verified-prebuilt path. The same reason now appears when `dot update` falls back to the full maintenance gate. (#276)
- `dot sync status` shows live conflicts, the ones the last push refused on, apart from retained backup directories and their size. (#277)
- Scheduled `dot peer sync` runs write dated start and exit lines to `.dotfiles/peer/log/sync.log`, and the log rotates at 10 MB, keeping three old files. (#275)
- `dot admit status` lists a running `dot peer sync` or `dot sync push` with its start time. (#274)

### Fixed
- A running `dot peer sync` or `dot sync push` no longer defers builds, tests or `dot update` in other repositories as uncovered heavy work. (#274)
- `dot sync push` no longer copies every routine update into the mirror's `.sync-conflicts`; it keeps deletions, overwrites of a copy changed in the mirror, and, to be safe, files that turned into a directory or link or that it cannot prove unchanged. Backups from earlier pushes stay until `dot sync conflicts prune`. (#277)
- A mirror file the cloud rewrote with the same bytes and a new timestamp no longer blocks `dot sync push` as a conflict. (#277)
- `dot peer sync` no longer fails at "detecting linked worktrees" when a directory disappears during the scan or an excluded directory is unreadable, and a file or folder deleted mid-run no longer stops it before the push. An unreadable directory that does sync is reported with its path and the fix. (#275)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.42...v2.70.43

## v2.70.42

dotfiles moves from entelecheia/dotfiles-v2 to STAIxBWLB/dotfiles, and the Homebrew formula moves from the entelecheia/tap tap to staixbwlb/cask.

### Improved
- `dot update`, the curl installer and Homebrew now point at STAIxBWLB/dotfiles and `staixbwlb/cask/dotfiles`. Files `dot` manages keep their existing names and markers, so they do not change on existing hosts. (#257)

### Action needed
- Homebrew users: run `brew update`, which migrates the install to the new tap. If `brew info dotfiles` still shows entelecheia/tap, run `brew trust staixbwlb/cask` and then `brew reinstall staixbwlb/cask/dotfiles`. (#257)
- Curl-installer users need nothing: `dot update` follows the move. The first update from an older version may wait once for the full maintenance resource check. (#257)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.41...v2.70.42

## v2.70.41

The global git ignore file now covers Kimi Code's working directory.

### Improved
- The global git ignore file written by `dot apply` ignores `.kimi-code/`, alongside the other AI agent tool directories. (#254)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.40...v2.70.41

## v2.70.40

`dot ai memory` reports MCP adapters correctly when `dot` runs through a symlink and repairs orphaned memory references on import.

### Fixed
- `dot ai memory status` reported Kimi, Qwen, Kiro and Copilot MCP adapters as missing when `dot` ran through a symlink such as `~/.local/bin/dot` while the adapters pointed at `/opt/homebrew/bin/dot`. Both paths are now recognized as the same executable, and your MCP configuration is left unchanged. (#252)
- `dot ai memory sync` skipped an incoming session that matched an existing one under a different memory ID, leaving its observations orphaned. Such references are now relinked to the existing session in one transaction, and the sync result shows the relink counts. (#252)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.39...v2.70.40

## v2.70.39

`dot peer sync` now propagates deleted symlinks in tracked home paths.

### Fixed
- A symlink deleted from a tracked home path, such as a skill link under `~/.claude/skills`, came back on the next `dot peer sync`. Symlinks, including dangling and directory links, now get baseline entries and deletion tombstones like files, under the same quarantine and deletion cap. Link targets are never followed, and a path through a symlinked parent directory is refused. (#250)

### Action needed
- Upgrade `dot` on both peers (`dot update` or `brew upgrade dotfiles`). (#250)
- Deletions propagate only for paths listed in `home-paths-tracked.txt`; move a path there from `home-paths.txt` if you want its deletions shared. (#250)
- The first complete sync records the links that exist now. Links deleted before that are not detected. (#250)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.38...v2.70.39

## v2.70.38

The global git ignore file now covers brag video output, and `dot` is built with a Go release that fixes standard-library vulnerabilities.

### Improved
- The global git ignore file written by `dot apply` ignores `brag-output/` and timestamped `brag-output-*/` directories, so video renders no longer show up as untracked files. (#247)

### Fixed
- `dot` is built with Go 1.26.9, which fixes standard-library vulnerabilities in `net/http` and other packages. (#248)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.37...v2.70.38

## v2.70.37

`dot peer sync` can now send Telegram alerts when peer sync fails.

### New
- With `watchdog.notify.telegram.enabled: true` and `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID` in `~/.config/dot/telegram.env` (managed by `dot secrets`), `dot peer sync` sends one alert when sync starts failing or holds changes, a reminder every `reminder_interval` (default 30m) while it persists, and one recovery message. An unreachable peer never alerts, `--dry-run` sends nothing, and an alert failure never changes the sync result. (#245)
- `quarantine_threshold` warns when one peer run quarantines more deletions than the limit. (#245)

### Improved
- The watchdog notifier tries every configured channel (macOS, ntfy, Telegram) even when an earlier one fails. (#245)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.36...v2.70.37

## v2.70.36

`dot update` no longer waits behind a memory warning for a verified release, and `dot admit` judges host pressure from continuous samples only.

### New
- `dot update --homebrew` lets a standalone `dot` binary upgrade the installed Homebrew formula. (#242)

### Improved
- `dot update` installing a checksum-verified prebuilt release can run under a memory warning when CPU, load and thermal state are healthy and the host is not swapping. Critical memory pressure, thermal pressure, missing telemetry and a recent WindowServer watchdog event still defer it, and other heavy work keeps the full check. (#242)

### Fixed
- `dot admit` could count two samples far apart as sustained pressure or as five minutes of recovery. Sustain and recovery now require fresh samples no more than 45 seconds apart, and `dot admit status` no longer shows a countdown across a stale gap. (#242)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.35...v2.70.36

## v2.70.35

`dot sync` now uses rsync 3.x for mirror pushes that delete files, and handles path names with spaces and newlines.

### Fixed
- A mirror push with delete propagation could leave planned deletions undone under macOS's built-in openrsync. `dot sync` now selects rsync 3.x for those pushes and refuses before any transfer when only openrsync is available; install it with `brew install rsync` if you see that refusal. `dot sync status` lists only the rsync clients that are actually available. (#240)
- Submodule paths and intake file lists with spaces or newlines in their names are read correctly. (#240)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.34...v2.70.35

## v2.70.34

Each host now decides on its own whether `dot apply` installs the container backend.

### Improved
- `dot apply` from a terminal asks an undecided host whether to install the container backend; answering No records the choice and later runs skip the module. `dot container setup` turns it on and overrides an earlier No. (#239)
- `dot apply --yes` and runs without a terminal never install on an undecided host; they print one line naming `dot container setup`. `dot container status` shows set up, declined or not chosen. (#239)

### Fixed
- Opting in on one Mac synced through `~/.config/dotfiles` to the other, whose next `dot apply` installed Apple's `container` unasked. The decision is now stored per host. Hosts already set up keep working. (#239)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.33...v2.70.34

## v2.70.33

A new opt-in container module gives you one `container` command on macOS and Linux.

### New
- `dot container setup` and `dot container status` set up and report an opt-in container backend. Every host then accepts Apple `container` syntax through a `container` shim on your PATH. (#237)
- On Apple silicon Macs with macOS 26 or later, setup installs Apple's `container` with Homebrew, starts its service and installs the recommended kernel. Other Macs are reported as unsupported and nothing is written. (#237)
- On Linux, setup uses a working docker as is, otherwise offers the distro docker package (`--yes` accepts) and falls back to podman. Without sudo, or non-interactive without `--yes`, it prints the commands and stops. Verbs and flags are translated for docker or podman, and `modules.container.dns` is passed as `--dns` to `run` and `create`. (#237)

### Action needed
- This version adds container settings to your user state, and older `dot` versions refuse to overwrite it. Upgrade `dot` on every host that shares `~/.config/dotfiles` (`dot update` or `brew upgrade dotfiles`). (#237)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.32...v2.70.33

## v2.70.32

`dot update` now upgrades a Homebrew install through brew instead of refusing.

### Improved
- On a Homebrew install, `dot update` checks the latest GitHub release first and does nothing when you are current. Otherwise it runs `brew update` only when the tap lags the release, then `brew upgrade`, and checks the new `dot --version`. A pinned formula is left alone. (#236)
- After a Homebrew upgrade, `dot update` reloads the loaded `com.dotfiles.*` LaunchAgents that run the upgraded binary, so scheduled sync and peer jobs no longer end up in `spawn failed`. A paused job stays unloaded, and Ctrl-C no longer interrupts brew or leaves a job unloaded. (#236)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.31...v2.70.32

## v2.70.31

`dot sync status` now names a scheduler that launchd cannot start, and `dot update` no longer overwrites a Homebrew-managed binary.

### Improved
- `dot sync status` shows a scheduled job that launchd left in `spawn failed` (for example after `brew upgrade dotfiles`), with its last exit code and the command that reloads it, instead of only reporting a stalled push. (#234)
- `dot sync status --json` (schema 6) and `dot peer status` report a refused `.sync-conflicts` directory instead of a conflict count of 0. (#234)

### Fixed
- `dot sync pause` now unloads a job in `spawn failed`, so `dot sync pause && dot sync resume` brings it back. (#234)
- A canceled SSH push no longer records a completed push. (#234)
- An exclude pattern with a slash and a wildcard, such as `foo/*.log`, now matches `a/foo/x.log` in the push plan and `dot sync fetch`, as rsync does. Before, the plan and fetch could report files rsync never sent. (#234)
- `dot update` on a Homebrew install stops before any download and points to `brew upgrade dotfiles`, instead of replacing the binary inside the Homebrew Cellar. `dot update --check` still reports the latest version. (#234)

### Action needed
- If scheduled sync stopped after a Homebrew upgrade, run `dot sync pause && dot sync resume` (or `dot peer setup` for peer sync) once. (#234)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.30...v2.70.31

## v2.70.30

`dot update` works again on current releases, and `dot sync` gets several safety fixes around backups and fetch.

### Fixed
- `dot update` stopped every update with `archive entry "dot" exceeds per-entry limit`, because the binary had grown past 16 MiB. The limit is now 64 MiB. Homebrew installs were not affected. (#232)
- `dot sync` pulls, fetch backups and `dot sync conflicts prune` refuse a symlinked `.sync-conflicts`, so they never write or delete outside the workspace, and `dot sync status` reports the refusal instead of "no backups". (#232)
- `dot sync fetch` reports a path a filter keeps out (a shared folder, an unallowed secret, a submodule, `.git`) as excluded instead of fetched, and accepts an absolute path inside the workspace. (#232)
- An interrupted push records a failure and skips the post-push steps. Shared entries dropped by path cleaning are listed, counted and removable with `dot sync shared`. (#232)

### Action needed
- If you installed with the curl installer and run v2.70.29 or earlier, `dot update` cannot reach this release. Rerun the installer once: `curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash`. Homebrew users run `brew upgrade dotfiles` as usual. (#232)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.29...v2.70.30

## v2.70.29

`dot sync` no longer gets stuck refusing every mirror push, cleans up renamed leftovers in the mirror, and syncs file names with wildcard characters correctly.

### Improved
- `dot sync status` warns when the last push failed or no push completed for three push intervals, with the error, when the refusals started and the next step. A refused or failed scheduled push is no longer visible only in `sync.log`. (#226)
- A failed push is reported by program, exit status and stderr instead of the whole rsync command line. (#226)

### Fixed
- A file the push copied to the mirror and the workspace then removed (for example a renderer's scratch frames) blocked every later mirror push as a conflict until a manual `--mode=force`. The baseline now records what the push sent, so the next push deletes it with a backup. (#226)
- A folder renamed in the workspace to drop a trailing period or space left its old copy in the mirror forever, and the plan kept listing it as a local name. With delete propagation on, the push now moves old copies the baseline proves came from the workspace into `.sync-conflicts`; others are listed under their own heading. (#226)
- File names containing `[`, `*` or `?` in tracked paths, shared entries, submodule paths and `dot sync fetch` are matched literally. Such a tracked file was neither uploaded nor deleted, and `a*b.md` could let an untracked `aXb.md` through. (#229)
- A shared folder is kept out of the push even when an allow-list entry such as `/team/ops/.env` opens its parent, and hand-edited `team//ops` shared entries or `.gitmodules` paths no longer fail every run. (#229)
- Pressing Ctrl-C at a push prompt counts as a decline, not a failed push. (#226)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.28...v2.70.29

## v2.70.28

`dot peer git realign` stops suggesting pushes over pre-rewrite history and checks the remote before a rescue push.

### Fixed
- For an `ahead-unpushed` repo whose history holds pre-rewrite commits, realign now warns and asks you to check which remote branch holds each twin, instead of suggesting `git push`. (#222)
- Before a rescue push, realign reads the remote branches the rescue rests on with `git ls-remote`. When one moved since the last fetch, is gone or cannot be read, it writes nothing and names the step that clears it: one `git fetch --multiple` for every moved remote, `--no-push` for a branch the remote no longer has, or checking an unreadable remote. (#222)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.27...v2.70.28

## v2.70.27

`dot peer git realign` now recognizes a rewritten upstream and never pushes a rescue that would republish the old history.

### New
- `dot peer git realign` reports a diverged repo whose local-only commits all have an upstream twin (same author, date and subject) as `rewritten-upstream`. Its rescue stays local: the suggestion is `--rescue --no-push --apply`, with a note that moved tags need `git fetch --tags --force`. (#218)
- `--candidate-refs <pattern>` (repeatable) adds commits from matching refs, for example the fetched unpushed branches of a Mac that stopped, as move targets. A target found only through such a ref is named in the output. (#218)

### Fixed
- A rescue push is refused whenever any commit it would send has a pre-rewrite twin on a remote, so a real commit on top of old history or a branch cut from the old default branch no longer publishes what the rewrite took out. Signed commits are now matched too. (#218)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.26...v2.70.27

## v2.70.26

A pushed rescue from `dot peer git realign` now picks a branch name the remote does not already have.

### Fixed
- `dot peer git realign --rescue` failed the push when the other Mac had already pushed a rescue branch with the same name that day. The free name now also skips names the push remote holds. (#215)
- A `rescue` branch on the push remote is reported as a refusal that `--no-push` lifts, and a failed rescue push says to run `git fetch <remote>` and then rerun, instead of a bare retry that failed the same way. (#215)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.25...v2.70.26

## v2.70.25

`dot peer git realign` now sees a branch held by a rebase or bisect in another worktree and handles a branch named `rescue`.

### Fixed
- `dot peer git realign` no longer suggests moving a default branch that a rebase or bisect in another worktree holds, which git then refused. It names the worktree and says to finish or abort there first; for a worktree whose directory is gone it names the admin directory to recover before removal, so a rebase's commits and autostash are not lost. (#213)
- A local branch named `rescue` blocked every `rescue/<date>-<branch>` name. Realign now reports it as a refusal with the rename step. (#213)
- Worktree detection works for worktrees added with `--relative-paths` and for repos whose path contains `[`, `*` or `?`. (#213)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.24...v2.70.25

## v2.70.24

`dot peer git realign` now only suggests rescue commands the run will accept, and its refusal advice actually clears the refusal.

### Fixed
- `dot peer git realign` no longer recommends `--rescue --apply` for a repo it then refuses to rescue. A Git LFS repo gets `--rescue --no-push --apply`, and every refusal that applies is listed at once, numbered, with its own step, instead of one per run. (#210)
- The refusal steps now lift the refusal: a local default branch with extra commits is moved aside to a free `<branch>-kept` name, a missing worktree is removed with `git worktree remove <path>` (after an unlock only when it is locked) instead of a `git worktree prune` that drops every missing worktree, and the commands run with `git -C <repo>` so submodules work. (#210)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.23...v2.70.24

## v2.70.23

`dot peer` gains a two-Mac health check, itemized sync plans and one-command renames, plus many fixes to `dot peer git realign`.

### New
- `dot peer doctor` checks both Macs before they sync again: rsync on each side, non-NFD names, roles and epochs, where the scheduler runs, the takeover replica and config keys that differ, with the fix and the host to run it on. `dot peer status` shows this Mac's role. (#193, #207)
- `dot peer diff` and `dot peer sync` take `--list` and `--json` to show every planned action with its direction, scope and sizes, next to the `max_delete` limits. Frequently changing host files are protected from being overwritten. (#194)
- `dot sync owner --rename <old> <new>` renames a Mac in every profile store and on the peer, keeping the old name as an alias. (#191, #198)
- `hooks.on_deactivate` and `hooks.on_activate` in `.dotfiles/peer/config.yaml` pause and resume launchd jobs or apps when a Mac hands over or takes over the coordinator role. (#192)
- `remote_dot` in `.dotfiles/peer/config.yaml` pins which `dot` binary runs on the peer. (#188)

### Fixed
- `dot peer` and `dot ai memory sync --peer` picked the first `dot` on the peer's path, so a stale dev build in `~/.local/bin` shadowed the Homebrew release. They now use the newest release and warn when only a dev build exists. (#188, #199)
- In a non-login shell `dot peer diff` and `dot peer sync` used macOS openrsync and rejected valid non-ASCII names as "not NFD-normalized". They now require rsync 3.x and say so before transferring. (#186)
- `dot apply` without a terminal uses the stored profile and identity (or `DOTFILES_PROFILE`, `DOTFILES_NAME`, `DOTFILES_EMAIL`) instead of failing in a prompt, and names the missing value when there is none. (#190)
- `dot peer git realign` picks the right commit when candidates tie, repairs `.gitmodules`, fetches a submodule's missing commit before judging it under `--fetch`, runs no repository hooks, and refuses a pushed rescue in a Git LFS repository. (#189, #205, #208)
- A peer that recorded a higher epoch with no owner made every scheduled run stop the coordinator's launchd jobs and then fail; `dot peer sync` now refuses and prints the `dot peer adopt` command that fixes it. (#206)
- Saving the peer config with an older `dot` dropped keys it did not know. They are now kept, and `dot peer handover` warns before handing over to a peer on an older release. (#200)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.22...v2.70.23

## v2.70.22

`dot peer` can now hand the coordinator role between Macs and realign Git state after a sync, and the co-author guard actually blocks commits.

### New
- `dot peer git status` and `dot peer git realign --apply` move each repository's HEAD and index to the commit its synced files already match, without touching your working tree. (#170)
- `dot peer handover` and `dot peer takeover` move the coordinator role between Macs, planned or not, using a replica of the sync baseline and an epoch fence so two Macs never both act as coordinator. (#172)

### Improved
- `dot peer sync` detects linked git worktrees anywhere in the workspace by their content, not only under known folders, and both Macs exclude the same set. Submodules keep syncing. (#171)
- `dot admit --wait` retries when another check briefly holds the gate instead of exiting 75 at once, and sends one alert per pressure episode instead of one per deferral. (#169)

### Fixed
- `dot ai coauthor-guard` installed a commit-msg hook that nothing ran. It is now wired through git's config-based hooks (git 2.54 or later), so it runs alongside each repository's own hooks, and it blocks by default. A dot-managed global `core.hooksPath` is removed. (#174)

### Action needed
- Run `dot ai coauthor-guard apply --persist` (or `dot apply`) once after upgrading so the hook is registered in `~/.config/git/config`. (#174)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.21...v2.70.22

## v2.70.21

`dot admit` stops deferring every job on macOS, and `dot ai run` and `dot admit` now share one set of slots.

### Fixed
- On macOS the thermal probe always timed out, so `dot admit` deferred every command with exit 75. It now reads the thermal state directly, and `dot admit status` prints it by name. (#164)
- `dot admit` now sees WindowServer watchdog reports, which macOS writes to `/Library/Logs/DiagnosticReports` and its `Retired/` folder rather than your user folder. (#166)
- `dot ai run` and `dot admit` could each run a heavy job in the same repository at once. They now share slots and pressure history, `--home` can no longer open a second slot, and a guarded job's child commands run in the parent's slot instead of waiting on it. (#167)
- `dot ai memory sync --serve import` restarted the claude-mem worker of the real home even when `--home` pointed elsewhere. (#163)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.20...v2.70.21

## v2.70.20

`dot ai` can now install and update the gencode, pi and Antigravity agent CLIs.

### New
- `gencode`, `pi` and `antigravity` (`agy`) are opt-in selections for `dot ai setup`, `dot ai tools` and `dot ai update`. Antigravity updates itself, so `dot ai update` reports it as deferred instead of reinstalling it. (#159)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.19...v2.70.20

## v2.70.19

`dot ai` now manages the agent CLIs you select, and new `dot admit` serializes heavy jobs per repository while the Mac is under load.

### New
- `dot ai tools` installs, updates and reports the agents you select (Claude, Codex, Kimi, Qwen, Grok, OpenCode) and optional add-ons (ripwire, Open Code Review, GSD, claude-mem, ponytail), keeping each tool's native installer and your pins. `dot ai handoff` records and shows curated handoffs between sessions, and `dot ai update schedule enable` turns on an opt-in weekly maintenance run. (#151)
- `dot admit -- <command>` runs a build or test only when no other heavy job holds the same repository (across all its worktrees) and memory, thermal, CPU and WindowServer pressure are normal; otherwise it waits up to `--wait` or exits 75. `dot admit status` shows who holds each slot and why the gate defers. `dot ai run` uses the same guard. (#158)
- `dot ai policy` (`inspect`, `resolve`, `diff`, `apply`, `status`, `rollback`) and `dot ai session start` add opt-in AI work policies with automatic review routing for Claude Code and Codex, applied as owned overlays you can roll back. (#155)
- `dot watchdog` adds monit checks for system load, CPU and Screen Sharing (with a Screen Sharing self-heal), and can run a Beszel monitoring agent whose key file `dot secrets` backs up encrypted. (#154, #156)

### Improved
- The managed global gitignore also ignores `.firecrawl/` scrape output. (#157)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.18...v2.70.19

## v2.70.18

This release adds `dot watchdog` for unattended Macs, syncs claude-mem between machines, and takes over your global git ignore file.

### New
- `dot watchdog` (opt-in per host): `setup`, `status`, `reap`, `notify`, `log` and `uninstall`. It finds runaway orphaned processes and, once you switch from dry-run to enforce, stops them; it can also self-heal Cloudflare WARP connectivity and apply headless power settings that `uninstall` restores. Enable it with `watchdog.enabled` in your profile. (#145, #148)
- `dot ai memory sync --peer <host>` copies the claude-mem store between two Macs over ssh, needing only `dot` on the other side, and can run hourly. `dot ai memory update` and a new step in `dot ai update` keep claude-mem at the latest version on both machines. (#149)
- The `git` module deploys a managed `~/.config/git/gitignore.global` and points `core.excludesFile` at it. It no longer ignores `*.sql` globally and keeps `.envrc`, `.env.example` and similar files visible. (#143)

### Improved
- `dot ai agents` no longer writes the shared instructions to `~/.cursor/AGENTS.md`; an existing dot-managed copy is removed. (#152)

### Fixed
- File names ending in a space or period, which Dropbox cannot store, made `dot sync push` refuse in a loop. They are now skipped and listed under "Unsupported names", and `dot sync names trim` renames them. (#144)

### Action needed
- If you kept edits in the old `~/.config/git/ignore`, move them to `~/.config/git/gitignore.global`. `dot apply` removes the old file only when it is unchanged and warns otherwise. (#143)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.17...v2.70.18

## v2.70.17

`dot ai agents apply` keeps its state per machine, so syncing two Macs no longer confuses it.

### Fixed
- The record of what `dot ai agents apply` last wrote moved from the synced `~/.config/dotfiles/agents/.state.json` to `~/.local/share/dotfiles/agents/state.json`. A copy synced from another Mac no longer makes this machine report drift or skip updates. The old file is migrated and removed on the next apply. (#140)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.16...v2.70.17

## v2.70.16

`dot sync` and `dot peer sync` no longer copy linked git worktrees.

### Fixed
- Worktrees under `.claude/worktrees`, `.qwen/worktrees` and `.worktrees` are always excluded. Removing a batch of agent worktrees used to exceed `max_delete` and stall every scheduled `dot peer sync`, and worktrees spread to the other Mac and the cloud mirror as copies without Git data. Old baseline entries under those folders are dropped without deleting or pulling anything. (#136)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.15...v2.70.16

## v2.70.15

`dot ai` now supports the pi and Qwen Code agents.

### New
- pi: `dot ai agents apply` writes the shared instructions to `~/.pi/agent/AGENTS.md`, `dot ai memory` feeds pi sessions into the claude-mem transcript bridge, `dot ai update` runs `pi update`, and `dot ai backup` covers the pi agent home, including `auth.json` with `--include-auth`. (#134)
- Qwen Code: `dot ai agents apply` writes `~/.qwen/AGENTS.md`, Qwen shares the claude-mem store through the memory MCP server and the transcript bridge, `dot ai update` runs `qwen update`, and `dot ai backup` covers its instructions and settings. (#134)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.14...v2.70.15

## v2.70.14

The managed shell config now points agent-hub at a cargo-installed `switchyard-server`.

### Improved
- `~/.config/shell/30-ai.sh` exports `AGENTHUB_SWITCHYARD_BIN` when `~/.cargo/bin/switchyard-server` exists, so agent-hub finds it without `~/.cargo/bin` on your `PATH`. Run `dot apply` to pick it up. (#132)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.13...v2.70.14

## v2.70.13

`dot ai memory install` now repairs the Codex claude-mem plugin, and archive extraction works again with Go 1.26 builds.

### Fixed
- `dot ai memory install` installs the missing runtime in the Codex claude-mem plugin cache, and the repair hint now names that command. `dot ai memory status` shows `CODEX_HOME` when it differs from `~/.codex`, so you can see where Codex installed the plugin. (#128)
- Installing oh-my-zsh and other archives failed with `mkdirat ...: no such file or directory` on directory entries that end in `/`. They extract again. (#130)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.12...v2.70.13

## v2.70.12

`dot peer` can now sync selected home paths with deletes and conflict protection, and peer previews no longer write to the sync store.

### New
- `home-paths-tracked.txt` lists home paths that `dot peer sync` keeps in step both ways, including deletions. Edits made on both Macs keep one version in place and move the other to `~/.dot-peer-conflicts/<timestamp>/`. `dot peer init` seeds it with the Claude auto-memory folder; manage it with `dot peer home-paths tracked`. (#126)

### Fixed
- `dot peer diff --dry-run` and `dot peer sync --dry-run` no longer create files in the workspace sync store. (#126)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.11...v2.70.12

## v2.70.11

claude-mem integrations for non-Claude agents keep working after Claude Code cleans up old plugin caches.

### Fixed
- `dot ai memory` locates claude-mem through Claude Code's `installed_plugins.json` and skips caches marked orphaned. Before, the bridge and the Kimi, Kiro and Copilot memory servers pointed at an orphaned cache that Claude Code deletes after 14 days. (#125)
- The memory bridge logs a repeated failure once instead of on every rescan, and its log is reset when it grows past 10 MB. (#125)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.10...v2.70.11

## v2.70.10

Sync no longer breaks on file names that end in whitespace, such as the macOS `Icon\r` file.

### Fixed
- `dot sync` and `dot peer sync` trimmed trailing whitespace from path names, so the `Icon\r` file Finder creates for custom folder icons became an unknown name, rsync exited 23 and every later peer run failed the same way. Names are now kept byte for byte, and the shipped `Icon?` exclude matches again. (#122)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.9...v2.70.10

## v2.70.9

`dot sync push` can reach "No push changes" again when empty files have evicted cloud copies.

### Fixed
- Empty local files such as `.gitkeep` whose mirror copies were online-only placeholders were planned as updates on every run. They are now skipped when the last push proves they were shipped empty; without that proof the transfer still runs. (#121)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.8...v2.70.9

## v2.70.8

`dot sync push` no longer piles up false conflicts from Dropbox online-only files.

### Fixed
- Online-only (evicted) files in Dropbox, iCloud or OneDrive were read as empty content, so every later local edit looked like a conflict until `dot sync push` refused with dozens of conflicts and the mirror stopped advancing. `dot` now recognizes cloud placeholders by their provider marker and treats them as evictions. A genuinely empty file behaves as before. (#119)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.7...v2.70.8

## v2.70.7

`dot peer status` shows when peer sync really last ran, and claude-mem picks up Kimi 0.40 sessions.

### Fixed
- `dot peer status` reported stale last-push and last-pull times because `dot peer sync` never recorded them. Each run now stamps the legs it completed, and a run that held back destructive changes shows a `Held transitions` line (`lastHeldAt` in `--json`). (#117)
- The claude-mem transcript bridge in `dot ai memory` finds and finalizes Kimi 0.40 sessions, which moved their workspace and completion fields. (#115)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.6...v2.70.7

## v2.70.6

`kimi-yolo` is full-access again on kimi-code 0.40 and later.

### Fixed
- The `kimi-yolo` shell helper starts Kimi with `--auto` (Never Ask mode). Since kimi-code 0.40, `--yolo` means Ask When Needed, so the helper kept stopping for confirmations. Run `dot apply` to refresh `~/.config/shell/30-ai.sh`. (#113)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.5...v2.70.6

## v2.70.5

`dot apply` can again refresh managed files on machines that have AI agent, app-settings or profile backups.

### Fixed
- Writes that need a backup no longer fail with `hardening backup entry ".../backup/agents": must be a regular file` after you have run `dot ai agents apply`, an app-settings backup or a profile restore. Managed files such as `~/.config/shell/30-ai.sh` stayed frozen at their last good version until this fix. (#112)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.4...v2.70.5

## v2.70.4

New shared AI agent instructions tell agents to write non-ASCII text as literal characters.

### Improved
- `dot ai agents init` seeds a default instruction that agents emit non-ASCII text (Korean, accents, symbols) as the literal characters in tool calls, file writes and shell commands, never as `\uXXXX` or other escapes. It applies to instruction files that `init` creates from now on. (#110)

### Action needed
- An existing shared instruction file is not changed. To get the new rule, add a Text Encoding section to it yourself and run `dot ai agents apply`. (#110)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.3...v2.70.4

## v2.70.3

`dot peer setup` and `dot peer status` now respect `--home` when talking to launchd.

### Fixed
- `dot peer setup` and `dot peer status` with `--home` no longer act on the invoking user's launchd domain, and `dot peer setup` on Linux is refused before it changes anything. (#109)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.2...v2.70.3

## v2.70.2

Maintenance release: no user-facing changes.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.1...v2.70.2

## v2.70.1

`--dry-run` on the sync and peer commands no longer touches your home or workspace.

### Fixed
- `dot sync push --dry-run`, `dot sync fetch --dry-run` and the other sync and peer previews no longer create `~/Library/Caches/dotfiles/` or populate the workspace sync store while printing "no changes". A preview run next to a live sync still reports the busy lock. (#101)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.70.0...v2.70.1

## v2.70.0

`dot` now verifies everything it downloads before installing it, honors `--home` and `--dry-run` everywhere, and makes `dot sync` show which secret files your allow list lets through.

### Improved
- The installer verifies the release archive against `checksums.txt` before extracting it and keeps the current `dot` binary in place if any step fails. Slow downloads that are still making progress are no longer cut off by a fixed total timeout. (#96)
- oh-my-zsh, the managed zsh plugins and Nerd Fonts are pinned to verified commits and SHA-256 digests. `dot apply` stages each one, checks it and rolls back on failure, and leaves your own files such as `custom/user.zsh` and unmanaged fonts alone. Archive extraction refuses entries that escape the target directory or exceed size limits. (#93, #94, #97, #98)
- `dot sync status` and `dot sync push --dry-run` list every `allow.txt` rule that overrides a built-in secret exclusion, and the default exclusions now cover more credential stores, private keys, certificates and keystores. Malformed sync targets are rejected up front. (#95)
- The `dot` state file records its schema version. `dot` refuses to overwrite a state file written by a newer release and tells you to run `dot update`; `DOT_SCHEMA_FORCE=1` overrides. The old `modules.ai_tools`, `modules.warp` and `terminal_apps.casks` keys are migrated automatically. (#88)

### Fixed
- `--home` (and `DOTFILES_HOME` for `dot init`) is honored by `dot status`, `dot sync`, `dot peer`, `dot secrets`, the schedulers, templates and SSH/age key discovery, which previously read or wrote the invoking user's home. (#91)
- `--dry-run` on `dot apply`, `dot sync init`, `dot peer init` and `dot peer setup` no longer writes state, creates a sync store or installs a launchd plist.
- A scheduled `dot peer sync` that finds another sync holding the lock now exits 0 and logs the skip instead of reporting a failure.
- `dot apply` exits non-zero when a module check fails instead of reporting success.
- `dot apps status` shows "unknown" when the Homebrew query fails instead of reporting every tracked app as not installed.
- `dot ai prune` and `dot profile prune` reject `--keep` values below 1 instead of confirming one number of deletions and performing another.
- Backups made just before a file is overwritten keep every version, even when two writes land in the same second, and scheduler units are validated before anything is replaced. (#89)
- Edits to Claude Code's `settings.json` are written atomically and keep changes another program made at the same time; a malformed file is reported as drift instead of stopping the run.

### Action needed
- If you run `dot peer sync` on a schedule, re-run `dot peer setup` once so the installed plist carries the marker that lets lock contention exit quietly.
- Scripts that read `dot sync status --json` should expect schema version 2, which adds the sensitive-override records. (#95)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.69.2...v2.70.0

## v2.69.2

`dot` no longer sends terminal queries that could leave stray text at your shell prompt.

### Fixed
- Running any `dot` command in an interactive terminal could leave text such as `11;rgb:2828/2c2c/3434` at the next prompt. The terminal color and cursor queries behind it are gone. (#67)

### Action needed
- Building `dot` from source now requires Go 1.25.8 or newer. Homebrew and release binaries are unaffected. (#67)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.69.1...v2.69.2

## v2.69.1

`dot ai hud` stops replacing a status line another tool installed, and `dot ai backup` no longer refuses to run on hook source code.

### Fixed
- `dot ai hud enable` and `dot ai hud apply` silently replaced a Claude Code `statusLine` installed by another tool. They now report it as a `conflict` naming the owner and take over only with `--force`; dot's own entry is tagged `# dot-hud`. (#65)
- `dot ai backup` failed with a false credential warning on JavaScript, Python and similar code such as `tokens[j]` or `token === '-C'`. Code files now pass, while shell, TOML, ini and `.env` files keep the strict check. (#65)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.69.0...v2.69.1

## v2.69.0

Adds a `kimi-yolo` helper and extends `dot ai update`, `dot ai list` and `dot ai backup` to more AI tools.

### New
- `kimi-yolo` starts Kimi with `kimi --yolo`, next to the existing `codex-yolo`, `claude-yolo`, `kiro-yolo` and `copilot-yolo`. (#64)

### Improved
- `dot ai update` also updates Kimi, Kiro and Cursor through their own self-update commands. (#64)
- `dot ai list` also shows Copilot, Kimi, Kiro and Cursor. (#64)
- `dot ai backup` includes `~/.kimi-code/config.toml`. (#64)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.68.3...v2.69.0

## v2.68.3

Peer sync stops deleting and recreating the same files on every run.

### Fixed
- `dot peer sync` deleted and recreated nested `.env` files allowed by `**/.env*` patterns in the peer `allow.txt` on every cycle, adding quarantine copies each time. They now stay in place. (#63)
- Continuously written runtime logs under `.maru/runs` and `.maru/desk-pipeline` output files are excluded from peer sync, so they no longer turn into conflicts when both machines write them. (#63)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.68.2...v2.68.3

## v2.68.2

`dot peer` reconciles two machines against their last sync instead of piling up overwrite backups, and adds a filename normalization command.

### New
- `dot sync names normalize --profile=peer` converts workspace filenames to the decomposed Unicode form (NFD) macOS uses, with a `--dry-run` preview and rollback on failure. After this one-time run, every push normalizes newly arrived names automatically. (#62)
- `dot sync conflicts prune --profile=peer --remote-only` cleans up conflict stores on the other machine while keeping your local backups. (#62)

### Improved
- `dot peer sync` and `dot peer diff` compare both machines with the last sync: one-sided edits and deletes flow in their direction, and only a real simultaneous conflict saves a backup under `.sync-conflicts/` and an entry in `.dotfiles/peer/peer-conflicts.log`. Routine runs no longer create timestamped backup folders. (#62)
- Cloud and peer sync share one workspace lock, so scheduled `dot sync` and `dot peer` jobs cannot change the tree at the same time. (#62)

### Fixed
- A partial rsync transfer now fails the peer sync and leaves the baseline untouched, instead of recording a sync that did not fully happen. (#62)

### Action needed
- Install this release on both machines (`dot update` or `brew upgrade dotfiles`) before the next peer sync. (#62)
- Both machines' peer profiles must name the same coordinator. If `dot peer sync` refuses on an owner mismatch, run `dot sync owner --profile=peer --set <coordinator>` on the other machine, and keep the peer scheduler enabled only on the coordinator. (#62)
- A push that finds filenames not yet in NFD form refuses and prints the fix: run `dot sync names normalize --profile=peer --dry-run`, then again with `--yes`. (#62)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.68.1...v2.68.2

## v2.68.1

Peer sync stops copying Codex's machine-local config, and the Codex HUD writes its config safely.

### Fixed
- `dot peer sync` no longer copies `~/.codex/config.toml` between machines, which broke Codex MCP OAuth logins on both sides. Existing `home-paths.txt` lists that still name the file are covered without edits. (#61)
- `dot ai hud apply` writes `~/.codex/config.toml` atomically, so Codex can no longer read a half-written config while the HUD is applied. (#61)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.68.0...v2.68.1

## v2.68.0

Codex setups managed by `dot` stop breaking after updates and restores, and `dot ai auth` now covers Codex MCP servers.

### New
- `dot ai auth status --tool codex` lists Codex MCP servers with their transport and auth state and points to `dot ai auth relogin --tool codex <server>` for repair; `--probe` runs `codex doctor`. (#60)

### Improved
- `dot ai update` checks Codex's claude-mem plugin copy after the marketplace upgrade and fails with the exact repair commands when its runtime went missing, instead of leaving memory silently broken. (#60)

### Fixed
- `dot ai memory status` reported green while the claude-mem runtime could not start, because dot picked an uninstalled plugin checkout. It now uses only a runnable install, and the Codex row turns red with repair commands when Codex's copy is broken. (#60)
- `dot ai restore` and `dot ai import` no longer overwrite a live `~/.codex/config.toml`, which left Codex MCP servers with stale OAuth logins. The live file is kept and reported; move it aside first to restore the snapshot copy. (#60)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.67.0...v2.68.0

## v2.67.0

GitHub Copilot CLI joins `dot ai memory`, with shared memory recall and transcript capture.

### New
- `dot ai memory` bridges GitHub Copilot CLI: Copilot can recall shared memory through MCP, and its session transcripts are captured into the claude-mem store like the other bridged tools. (#56)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.66.0...v2.67.0

## v2.66.0

Adds `dot ai update` to update AI CLIs, plugins and skills in one step, and `dot ai auth` to repair MCP server logins.

### New
- `dot ai update` updates Claude Code (CLI, marketplaces and plugins), Codex, Copilot, Gemini and skills in a fixed order. Missing tools are skipped, one failure does not stop the rest, and a summary prints before a non-zero exit. `--check` reports without changing anything, `--tool` picks a subset, and `--json` gives machine-readable results. (#54)
- `dot ai auth status` lists Claude Code MCP servers and the ones waiting for re-authentication (`--probe` adds live state), and `dot ai auth relogin <server> --yes` logs a server out and back in. `--tool codex` targets Codex instead. (#54)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.65.0...v2.66.0

## v2.65.0

GitHub Copilot CLI becomes a fully managed AI tool, with shared instructions, backups and a `copilot-yolo` helper.

### New
- `copilot-yolo` starts Copilot CLI with `copilot --allow-all`. (#52)

### Improved
- `dot apply` and `dot ai agents apply` write the shared agent instructions to `~/.copilot/copilot-instructions.md`, the file Copilot CLI 1.x reads, by default. The old `~/.config/github-copilot/AGENTS.md` is removed only when dot generated it. (#52)
- `dot ai backup` covers Copilot's `copilot-instructions.md`, `settings.json`, `mcp-config.json` and `permissions-config.json`. Backups made before this release still restore. (#52)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.64.0...v2.65.0

## v2.64.0

Adds `dot noindex` to keep Spotlight out of build and cache folders on macOS.

### New
- `dot noindex` places `.metadata_never_index` markers in build and cache folders such as `node_modules` under your projects, and one marker at the top of tool caches like `~/.npm` and `~/.local`. `dot noindex --dry-run -v` previews, `dot noindex setup` installs a LaunchAgent that repeats the sweep every 6 hours, and `dot noindex uninstall` removes it. `build/` and `out/` stay indexed so finished exports remain searchable. (#50)

### Fixed
- The `npm`, `pnpm`, `yarn` and `bun` wrappers are now defined once in `~/.config/shell/00-exports.sh`, so a later shell file can no longer shadow `pnpm` and drop the managed `NPM_CONFIG_USERCONFIG`. (#50)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.63.0...v2.64.0

## v2.63.0

Adds a versioned JSON interface to `dot sync` and `dot peer` for scripts and desktop apps such as Maru.

### New
- `dot sync status --json` and `dot peer status --json` print a versioned status document (`schemaVersion: 1`) with scheduler state, intervals, last exit code and run count, without contacting the peer over SSH. (#49)
- `dot sync filters get|set`, `dot peer home-paths get|set`, `dot sync configure` and `dot sync log --tail=N --json` read and edit sync settings from scripts. Edits are written atomically, and adding patterns to `allow.txt` requires `--ack-secret-exposure`. (#49)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.62.0...v2.63.0

## v2.62.0

`dot peer` now carries deletions to the other machine, moving removed files into quarantine instead of deleting them.

### New
- A file you delete on one machine no longer reappears on the next `dot peer sync`. The peer's copy moves to `.sync-conflicts/<timestamp>/from-workspace/`, where `dot sync conflicts prune` expires it. Only files recorded in the last successful sync qualify, so files created on the other machine are left alone. (#48)
- Peer profiles created with `dot peer init` turn this on and cap each run at `max_delete: 100`. Set `propagation.delete: false` in `.dotfiles/peer/config.yaml` to keep peer copies instead. (#48)

### Action needed
- Upgrade `dot` on both machines (`dot update` or `brew upgrade dotfiles`) before the scheduled peer sync runs again; an older peer can send a deleted file back. (#48)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.61.2...v2.62.0

## v2.61.2

`dot sync push` no longer fails or piles up conflicts when the cloud client keeps files online-only.

### Fixed
- `dot sync push` treated files a cloud client keeps online-only (rsync exit 23 or 24) as a failed push and left its baseline stale, so the skipped files came back as a growing list of mirror-only conflicts until the push refused to run. It now reports them as a warning and still updates the baseline. (#46, #47)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.61.1...v2.61.2

## v2.61.1

Fixes two failures that only showed up in scheduled runs or on fresh machines.

### Fixed
- Scheduled `dot sync` pushes failed every run with "profile ... is owned by ... but this machine answers to ..." while manual pushes worked, because the scheduler's limited PATH changed how the machine name was looked up. (#45)
- `dot ai memory install` on a machine with no Kimi or Kiro sessions wrote a transcript watch config that claude-mem rejected ("Invalid transcript watch config"), so transcript capture never started. Re-run `dot ai memory install` to rewrite it. (#45)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.61.0...v2.61.1

## v2.61.0

Adds sync profiles, a one-machine owner guard for the cloud mirror, and `dot peer` for two-way sync between machines.

### New
- `dot peer` keeps a workspace in sync both ways with another machine over SSH: `dot peer init --host user@host`, `dot peer doctor`, `dot peer sync`, `dot peer diff`, and `dot peer setup --interval=15m` to schedule it. Unlike the cloud mirror, it carries secrets (through its own `allow.txt`), submodule working trees, and files outside the workspace such as `~/.ssh` listed in `home-paths.txt`. An unreachable peer exits 0, so the schedule is safe on a laptop. (#44)
- `dot sync --profile=<name>` keeps independent sync stores under `.dotfiles/<name>/`, each with its own target, filters, baseline and scheduler. The default `sync` profile behaves as before. (#44)
- `dot sync owner --set-self` lets only this machine push a profile, so two machines cannot keep undoing each other's mirror pushes. (#44)

### Improved
- The default binary allowlist includes `.zip`, `.tar`, `.webp` and `.bmp` files. New sync stores get it by default. (#43)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.60.0...v2.61.0

## v2.60.0

Adds `dot sync fetch` for restoring specific paths, and keeps `dot sync push` working on Dropbox online-only files.

### New
- `dot sync fetch <path>...` restores specific files or folders from the sync target without a full pull. Newer local files are kept, overwritten files are backed up, nothing is deleted, and the usual secret and submodule filters still apply. It works with both the rsync that ships with macOS and Homebrew rsync. (#41, #42)

### Fixed
- `dot sync push` no longer forces Dropbox online-only files to download, or fails with timeouts, when it refreshes its baseline after a push. Unreadable subfolders are skipped with a warning instead of aborting the push. (#40)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.59.0...v2.60.0

## v2.59.0

`dot sync` becomes the single workspace sync command, with Git-aware filtering, local or SSH targets, and secrets excluded by default.

### New
- `dot sync` syncs your workspace to a target you set with `dot sync target local:~/Dropbox/work` or `dot sync target ssh:user@host:~/work`. It replaces both the old SSH-only `dot sync` and `dot gsync`. (#39)
- `dot sync filters show` prints the active filter layers in order, and `dot sync filters reset` regenerates the exclude and include lists from the defaults, keeping `.bak` copies. (#39)

### Improved
- The sync set follows Git: files tracked by the workspace's root repository plus untracked files that match the binary allowlist. Submodules are left to Git, and `.git` never syncs. (#39)
- The default exclude list also skips editor and OS leftovers, Dropbox conflicted copies and tool caches such as `.pnpm-store`, `.mypy_cache` and `.ruff_cache`. (#39)
- `dot sync status` shows the target, the number of submodules left to Git, and a warning whenever `allow.txt` re-includes secrets. (#39)

### Action needed
- `dot gsync` and `dot gdrive-sync` still work but are deprecated; switch to `dot sync`. The store moves from `.dotfiles/gdrive-sync/` to `.dotfiles/sync/` automatically on the first run. (#39)
- If you used the old SSH-only `dot sync`, point the new one at your remote with `dot sync target ssh:user@host:path`. (#39)
- Secrets (`.env`, `.env.*`, `.secrets`, `.maru/secrets`) no longer sync unless you list them in `.dotfiles/sync/allow.txt`. (#39)
- Automatic sync is off by default. To schedule it, run `dot sync setup --push-interval=10m --push-mode=clean` (and `--pull-interval` if wanted); this also removes the old `gdrive-sync` and `workspace-sync` scheduler units. (#39)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.58.0...v2.59.0

## v2.58.0

`dot apply` keeps working on Homebrew 6, and secrets backups now include the key that decrypts them.

### Fixed
- `dot apply` no longer stops on Homebrew 6 with "Refusing to load formula ... from untrusted tap"; it runs `brew trust` for every tap it installs from. Older Homebrew is unaffected. (#38)
- `dot backup --scope secrets` now includes the age identity that decrypts the secrets, so `dot restore --scope secrets` on a new machine no longer fails with "age identity not found". A full `dot backup --yes` also captures the age keys, which it used to skip. (#37)
- `dot restore` warns and names the fix when secrets are restored without the profile scope that carries the age key. (#37)

### Action needed
- Run `dot backup --scope secrets` (or a full `dot backup`) again so your existing backups contain the age key. (#37)
- If you installed `dot` from the `entelecheia/tap` Homebrew tap, run `brew trust entelecheia/tap` once. `dot apply` handles the taps it adds, but not the one that installs `dot` itself. (#38)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.57.1...v2.58.0

## v2.57.1

App settings backups now capture sandboxed macOS apps instead of saving empty folders.

### Fixed
- `dot apps backup` saved sandboxed apps such as Shottr and Yoink as empty `Containers/` folders, because one macOS metadata file that stays unreadable even with Full Disk Access aborted the copy. That file is now skipped on backup and restore. (#36)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.57.0...v2.57.1

## v2.57.0

Orca becomes the default terminal app in the `full` profile and installs automatically on macOS and Arch Linux.

### New
- A fresh `full` profile selects Orca instead of Warp. `dot apply` installs it with `brew install --cask stablyai/orca/orca` on macOS or `yay -S stably-orca-bin` on Arch-based Linux (requires `yay`), and skips the install when Orca is already there. This sets dot's terminal app choice, not the operating system's default terminal. (#35)

### Improved
- The terminal app prompt in `dot init` and `dot reconfigure` lists only apps available on your platform. Existing terminal selections are kept, and a saved `terminal_apps.casks` list is read as the new `terminal_apps.apps` key without edits. (#35)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.56.0...v2.57.0

## v2.56.0

Adds one-word full-access shell helpers for Codex, Claude Code and Kiro.

### New
- `codex-yolo`, `claude-yolo` and `kiro-yolo` start each CLI with approval and permission prompts turned off (`codex --dangerously-bypass-approvals-and-sandbox`, `claude --dangerously-skip-permissions`, `kiro-cli chat --trust-all-tools`). They live in `~/.config/shell/30-ai.sh`, so they survive `dot apply`. (#34)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.55.0...v2.56.0

## v2.55.0

You can now choose your default editor (Zed, VS Code or vi), and `dot` installs the one you pick.

### New
- `dot init` and `dot reconfigure` ask for a default editor (`zed`, `code` or `vi`), saved as `modules.editor`. It sets `$EDITOR`, and `dot apps` installs the matching Homebrew cask. (#33)
- Zed is available in the macOS app catalog. (#33)

### Action needed
- On macOS, `$EDITOR` now defaults to `zed --wait` instead of VS Code when no editor is chosen. To keep VS Code, run `dot reconfigure` and pick `code`. (#33)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.54.0...v2.55.0

## v2.54.0

The workspace shell environment now exports a shared scratchpad location for AI tools.

### New
- `dot apply` adds `MARU_SCRATCHPAD`, `MARU_TEMP` and `CLAUDE_CODE_TMPDIR` to `~/.config/shell/40-workspace.sh`, so Maru, Claude Code and other AI CLIs share one temporary-file root under `<workspace>/work/scratchpad`. (#32)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.53.0...v2.54.0

## v2.53.0

`dot ai memory install` no longer fails when it restarts the background service.

### Fixed
- `dot ai memory install` no longer fails with "Could not find service" (exit 113) when it replaces an already running memory bridge service on macOS.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.52.0...v2.53.0

## v2.52.0

New `dot ai memory` commands share one claude-mem memory store across Codex, Kimi Code and Kiro.

### New
- `dot ai memory install` connects Kimi Code and Kiro to claude-mem's recall server and runs a macOS background service that captures their sessions into `~/.claude-mem`, alongside Codex. `dot ai memory status` checks the setup. It needs Node.js and Bun; re-run `install` after moving the `dot` binary.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.51.1...v2.52.0

## v2.51.1

`dot apply` installs the renamed Maru Homebrew packages again.

### Fixed
- `dot apply` no longer fails with "No available formula with the name anchor-cli" or "Cask 'anchor' is unavailable"; it installs `maru-cli` and `maru-workspace`, and old `anchor` names in saved settings are mapped to the new ones. (#31)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.51.0...v2.51.1

## v2.51.0

dot now finds your vault where it actually lives instead of assuming `<workspace>/vault`.

### Improved
- `dot init` asks where the vault lives and saves it as `workspace.vault`; without a setting dot uses `<workspace>/work/vault` or `<workspace>/vault`, whichever exists. (#30)

### Fixed
- The `VAULT` shell variable no longer points at a missing folder when the vault is inside the work repository. (#30)
- `dot ws init` and `dot apply` no longer plan a second vault clone into `<workspace>/vault` when the vault already comes with the work repository. (#30)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.50.0...v2.51.0

## v2.50.0

`dot init` now lets you choose Dropbox or Google Drive for the workspace mirror.

### New
- `dot init` and `dot reconfigure` list detected Dropbox and Google Drive folders (including those under `~/Library/CloudStorage`) and let you pick one for the workspace mirror, enter another path or skip it. Unattended first runs prefer Dropbox; existing choices are kept. (#29)

### Action needed
- The workspace shell variables are renamed: `GDRIVE_WORKSPACE` is now `CLOUD_WORKSPACE`, `GDRIVE_WORK` is now `CLOUD_WORK` and the `gwork` alias is now `cwork`. Update scripts or shell habits that use the old names. (#29)
- When you switch clouds, old links such as `~/gdrive-workspace` and `<workspace>/work/.gdrive` stay in place; remove them by hand if you no longer need them. (#29)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.49.0...v2.50.0

## v2.49.0

`dot ai backup` now refuses to save inline secrets, Kiro and Kimi Code get shared agent instructions, and Anchor is renamed Maru.

### New
- `dot ai agents apply` renders shared instructions for Kiro (`~/.kiro/steering/AGENTS.md`) and Kimi Code (`~/.kimi-code/AGENTS.md`); bootstrap Kimi once with `dot ai agents apply --tool kimi`. Their files are included in `dot ai backup`. (#28)
- `~/.config/shell/50-tools-init.sh` adds opencode, kilo and kimi-code to `PATH` when installed, so their setup survives `dot apply` rewriting `~/.zshrc`. (#27)

### Improved
- `dot ai backup` and `dot ai export` stop with an error when a settings file contains a secret-looking value, instead of saving it. (#26)
- Claude MCP servers are backed up from `~/.claude.json`, and `dot ai restore` merges them into the existing file instead of replacing it; older snapshots are migrated on restore. (#26)
- `dot ai import` and `dot ai restore` reject archives with links, special files or unknown entries, and exports are written atomically. (#26)
- New `dot init` runs turn on shared agent instructions (`modules.ai.agents_ssot`) by default. (#26)
- The Writing Style section in new agent instruction files now prefers plain ASCII punctuation. (#25)

### Action needed
- If `dot ai backup` stops on a detected secret, move that value to an environment variable or keychain, or pass `--include-auth` for known auth files. (#26)
- Anchor is renamed Maru: backups now read `~/.maru/settings.json` and `~/.maru/sites.json`. Skills for `agents`, `gemini` and `antigravity` are scan-only and are dropped from managed targets with a warning. (#26)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v2.48.0...v2.49.0

## v2.48.0

This release starts the 2.x versions, adds Claude Code safety hooks and leaves skill linking to the Maru app.

### New
- `dot guard enable` installs Claude Code hooks that ask before destructive shell commands (`rm -rf`, `git push --force`, `git reset --hard`, SQL `DROP` and similar), and `dot guard freeze <dir>` blocks Claude Code's Edit and Write tools outside a directory (shell commands are not path-checked, and temp and plan folders stay writable). `dot guard status`, `unfreeze` and `disable` manage them. (#23)
- `dot ai agents init` adds a Writing Style section to new agent instruction files. (#22)

### Improved
- `dot ai skills list`, `validate`, `path` and `status` now only report; the default skills source is `~/.maru/skills`, and `anchor` still works as the provider name. (#24)

### Action needed
- `dot ai skills apply` is removed and `dot apply` no longer links skills; the Maru app manages skill links now. A `modules.ai.skills.enabled` setting still loads but is ignored. (#24)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.47.0...v2.48.0

## v0.47.0

New `dot tunnel` commands give SSH access to your Mac through Cloudflare Tunnel, and the old rclone and Drive-exclude commands are gone.

### New
- `dot tunnel setup` sets up SSH access to this Mac through a Cloudflare Tunnel: Remote Login, DNS route, configuration and a background service. `status`, `log` and `uninstall` manage it, and `dot tunnel client add|list|remove` writes SSH client entries on the machines you connect from. (#21)

### Improved
- `dot status` shows `dot gsync` state and the last push and pull. (#20)
- `dot gsync` works with any synced cloud folder, not only Google Drive. (#20)

### Fixed
- Two `dot gsync` or `dot sync` runs can no longer start at the same time, and a run blocked by the lock now exits with an error so schedulers notice. (#20)
- `dot ws` and `dot gsync` with `--home` now act on the target user's mirror instead of your own. (#20)
- `dot check` and `dot diff` now report pending GPG `gpg.format` changes. (#20)

### Action needed
- `dot clone` (rclone sync) and `dot drive-exclude` are removed; use `dot gsync` instead. `rclone` is no longer in the `full` profile. (#20)
- The `dot ws-mkdir`, `ws-mv`, `ws-rm`, `ws-audit` and `ws-reconcile` shortcuts are removed; use `dot ws mkdir`, `dot ws mv` and so on. (#20)
- `dot gsync shared` no longer detects Drive shortcuts on its own; add shared folders with `dot gsync shared add`. (#20)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.46.0...v0.47.0

## v0.46.0

dot now prefers Dropbox when it picks a default backup folder and `dot gsync` mirror.

### New
- `dot gsync mirror [path]` sets the mirror folder; without a path it prints the current one.

### Improved
- Backup folder detection (`dot backup`, `dot profile root --detect`) tries Dropbox before Google Drive when a `secrets/` folder is present there.
- The default `dot gsync` mirror is now `<cloud>/work` in the detected Dropbox or Google Drive folder.
- `dot secrets backup` works without a destination and writes to `<backup-root>/secrets-age/<host>`.

### Action needed
- If you have Dropbox with a `secrets/` folder and never set a mirror, `dot gsync` and backups may now point at Dropbox. Check with `dot gsync mirror` and set the path you want.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.45.0...v0.46.0

## v0.45.0

New `dot backup` and `dot restore` wizards move everything dot manages to a new machine in one run.

### New
- `dot backup` walks through profile, macOS app settings, AI settings and secrets in one interactive run and prints a summary; `--yes` with `--scope` runs it unattended.
- `dot restore` picks a source machine from the backup folder, then restores the profile, secrets, app and AI settings in the right order, with an optional `dot apply` in between.
- `dot profile`, `dot apps` and `dot ai` restore, list, status and prune commands accept `--host` to work with another machine's backups.
- `dot ai backup` now includes Anchor's `settings.json` and `sites.json`.

### Improved
- `dot secrets init` checks that each encrypted archive can be decrypted with your key, and an interrupted run no longer truncates the previous archive.
- Restores of profile, app and AI settings take a full backup of the current files first.
- A macOS app path that cannot be read no longer discards that app's entire backup; the last good copy is kept.

### Fixed
- `--dry-run` on backup, restore and secrets commands no longer writes files or changes permissions.
- `dot secrets restore` warns about archives that match no known secret instead of reporting success.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.44.0...v0.45.0

## v0.44.0

`dot update` now verifies downloads, `dot secrets restore` is safer, and `dot gsync` can clean up old conflict backups.

### New
- `dot gsync conflicts prune` deletes conflict backups older than 30 days (or `--older-than <days>`, or `--all`) on both the workspace and the mirror.

### Improved
- `dot update` checks the downloaded release against its published SHA-256 checksum and refuses to install on a mismatch.
- `dot secrets restore` decrypts to a temporary file first, so a wrong key or corrupt archive no longer destroys the existing file. A changed file is saved as `<file>.bak-<timestamp>` before it is replaced.
- `dot gsync pull` compares files by size and modification time first; `--strict` forces full hashing.

### Fixed
- `dot gsync pull` no longer silently overwrites a local edit that kept the same size and modification time; it records a conflict instead.
- `dot gsync status` now counts conflict backups on the mirror, not only in the workspace.
- `dot secrets restore` once again restores an empty secrets file and resets key permissions to 0600 when they have drifted.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.43.3...v0.44.0

## v0.43.3

`dot ai skills path` and `dot ai skills status` now work without any flags.

### Fixed
- `dot ai skills path` and `dot ai skills status` no longer fail with "skills provider is required" when run without flags; they default to the Anchor source and detect installed tools. `dot ai skills apply` still needs explicit tools, now with an error that says what to pass. (#15)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.43.2...v0.43.3

## v0.43.2

dot can now be installed and upgraded with Homebrew.

### New
- Install with Homebrew (`brew tap entelecheia/tap && brew install dotfiles` at the time; the formula now lives in `staixbwlb/cask`, see v2.70.42); the curl installer remains available.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.43.1...v0.43.2

## v0.43.1

This release adds a terminal app picker, Anchor app installs and skill deployment from a single source folder.

### New
- `dot init` and `dot reconfigure` on macOS let you pick terminal apps (`warp`, `wave`, `cmux`, `iterm2`) to install; the choice is saved under `modules.terminal_apps`. An existing `warp: true` setting carries over.
- The `full` profile installs the Anchor app and CLI from the `staixbwlb/cask` tap.
- `dot ai skills status` and `dot ai skills apply` link skills from one source folder (default `~/.anchor/skills`) into each tool's skills folder; enable it on `dot apply` with `modules.ai.skills`.

### Improved
- `dot apply` warns about skills in `~/.claude/skills` that are plain files instead of links.

### Fixed
- `dot apply` on Linux no longer fails trying to install macOS-only Homebrew formulas.

### Action needed
- `dot ai backup` no longer includes skill folders. Keep skills in their own source folder or repository if you relied on backups to carry them.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.42.0...v0.43.1

## v0.42.0

dot now manages Antigravity CLI settings next to Claude Code and Codex.

### New
- `dot ai backup` and `dot ai restore` include Antigravity CLI settings (conversation history is excluded).
- `dot ai agents apply --tool antigravity` renders shared agent instructions to `~/.gemini/GEMINI.md`; `gemini` is accepted as an alias.
- `dot ai skills list` and `validate` also scan Antigravity skill and plugin folders under `~/.gemini`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.41.0...v0.42.0

## v0.41.0

`dot gsync` pushes run faster and its status reporting is more accurate.

### Improved
- `dot gsync push` compares files by size and modification time first and only hashes them when needed, so pushes of large workspaces are faster.
- A `dot gsync push` that has nothing to send, or runs with `--dry-run`, now updates the last-push time, so monitoring no longer shows a stale run.

### Fixed
- `dot gsync status` no longer reports the sync lock as held forever after a sync process crashed.
- `dot gsync status` now shows the macOS scheduler state correctly instead of reporting it as not loaded.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.40.7...v0.41.0

## v0.40.7

New commands check AI skill files and show a log of AI setting changes, and `dot ai agents apply` no longer overwrites files you edited by hand.

### New
- `dot ai skills list` and `dot ai skills validate` inventory installed skills and check their `SKILL.md` metadata. (#14)
- `dot ai audit tail` and `dot ai audit summary` show a redacted log of changes dot made to AI settings. (#14)

### Improved
- `dot ai agents apply` no longer overwrites an instruction file that was edited outside dot unless you pass `--force`. (#14)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.40.6...v0.40.7

## v0.40.6

`dot apply` now handles Homebrew warnings and Bun installs on Linux more reliably.

### New
- Bun is available as an optional tool in the terminal tool selection; it is not installed by default on servers.

### Fixed
- `dot apply` no longer stops when `brew install` exits with an error even though the packages were installed.
- Installing Bun from its Homebrew tap on Linux no longer fails for lack of a compiler or `unzip`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.39.0...v0.40.6

## v0.39.0

New `dot ai hud` and `dot ai coauthor-guard` commands add an AI status line and a co-author trailer check.

### New
- `dot ai hud apply` installs a status line for Claude Code and Codex showing model, branch, context, token and usage-limit information. `dot ai hud status` shows the current setup, and `--persist` keeps it in sync on future `dot apply` runs.
- `dot ai coauthor-guard apply` adds a global Git `commit-msg` hook and an AGENTS instruction that warn about (or, with `--mode block`, reject) `Co-authored-by` trailers. Use `dot ai coauthor-guard status` to check it.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.38.3...v0.39.0

## v0.38.3

`dot gsync` drops the one-time `migrate` command and adjusts which file types it syncs by default.

### Improved
- `dot gsync` now syncs `.rar` and `.wmf` files by default and no longer syncs `.tsv` and `.html` files.

### Action needed
- `dot gsync migrate` is removed. If a script still calls it, remove the call.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.38.2...v0.38.3

## v0.38.2

`dot apply` now installs Node.js through fnm so `npm` works right after setup.

### Improved
- The `minimal` profile now includes `fnm` and the node module, so every profile gets Node.js.
- `dot apply` installs the Node.js LTS release with fnm when none is installed, then installs global npm packages through it, so a fresh machine no longer ends up without a working `npm`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.38.1...v0.38.2

## v0.38.1

`dot gsync push` no longer creates empty folders on the mirror.

### Fixed
- `dot gsync push` no longer creates empty directories on the mirror for folders whose files are all filtered out.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.38.0...v0.38.1

## v0.38.0

The workspace mirror command is now `dot gsync`; `dot gdrive-sync` keeps working as an alias.

### Improved
- `dot gsync` is the new name for `dot gdrive-sync`. The old name still works, and the existing schedulers, store directory and state carry over without migration.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.37.0...v0.38.0

## v0.37.0

`dot gdrive-sync` now syncs only binary and document files by default instead of the whole workspace.

### New
- `dot gdrive-sync push --filter-mode=exclude` runs one sync with the old broad behavior; set `filter_mode` in the workspace sync `config.yaml` to change it permanently.

### Improved
- `dot gdrive-sync` defaults to include-first sync: only patterns listed in the editable `include.txt` (archives, media, images, PDF, HWP and Office files) are mirrored, and Git-tracked files are always skipped.
- `dot gdrive-sync status` shows the active filter mode.

### Action needed
- If you relied on `dot gdrive-sync` mirroring every file, set `filter_mode: exclude` in the workspace sync `config.yaml`, or add your file types to `include.txt`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.36.0...v0.37.0

## v0.36.0

`dot` is now the main command name, `dot gdrive-sync` previews every push and pull before changing files, and automatic Drive sync is off unless you turn it on.

### Improved
- `dot` is the main command and binary; `dotfiles` keeps working as an alias. (#12)
- `dot gdrive-sync push` and `pull` list the files they will change and any conflicts, then ask before applying. `--mode=clean` applies only when there are no conflicts, and `--mode=force` overwrites and keeps a backup under `.sync-conflicts/<time>/from-workspace/`. `intake` no longer runs a pull first. (#12)
- `dot gdrive-sync setup` installs no scheduler by default and removes ones it installed earlier. Pass `--push-interval=DUR --push-mode=clean|force` and/or `--pull-interval=DUR --pull-mode=clean|force` to turn on automatic push or pull. (#12)
- Running `dot gdrive-sync` with no subcommand prints help instead of starting a push. (#12)

### Fixed
- `dot gdrive-sync` works with the rsync that ships with macOS; before, it needed a newer rsync from Homebrew. (#12)
- `dot gdrive-sync setup --dry-run` no longer installs or removes scheduler units. (#12)

### Action needed
- `dot update` on v0.35.0 or older cannot find this release, because the download is now named `dot_...`. Reinstall once with `curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash`; `dot update` works again after that.
- If you rely on automatic Drive sync, run `dot gdrive-sync setup --push-interval=5m --push-mode=clean` (and `--pull-interval` if you want pulls) after upgrading. Running `setup` without these flags removes the scheduler.
- The terminal app and tool pickers from v0.34.0 are removed again: `dot init` asks a single Warp yes/no question, saved `terminal_apps` and `terminal_tools` selections are ignored, and iTerm2 is no longer in `dot apps backup`. Install cmux and similar apps with `dot apps install`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.35.0...v0.36.0

## v0.35.0

`dot ai agents` keeps one shared instruction file for all your AI coding tools.

### New
- `dot ai agents` manages a single `~/.config/dotfiles/agents/AGENTS.md` and copies it to Claude, Codex and Cursor, with Gemini CLI, GitHub Copilot and Aider as optional targets and per-tool additions. Subcommands: `init` (optionally from a tool's current file), `author`, `show`, `edit`, `apply`, `pull`, `diff`, `list`, `status` (shows targets that drifted) and `path`. (#11)
- `dot ai backup`, `restore`, `export` and `import` include the shared file and its copies, and `dot ai restore --reapply-agents` copies the restored file back out to your tools. Set `modules.ai.agents_ssot: true` to have `dot apply` re-copy it on every run; it is off by default. (#11)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.34.0...v0.35.0

## v0.34.0

`dot init` lets you pick which terminal apps and command-line tools to install.

### New
- `dot init` and `dot reconfigure` replace the single Warp question with two pickers: terminal apps on macOS (Warp, cmux, iTerm2) and terminal tools on every platform (such as `fzf`, `ripgrep`, `bat`, `zoxide`, `lazygit`, `yazi`), plus any extra Homebrew formulas you type in. Each profile comes with sensible defaults, and an existing `warp: true` setting carries over. (#10)
- `dot apps list` shows a Terminal Tools section with each tool's install status, `dot config` shows how many apps and tools you picked, and `dot apps backup` can back up iTerm2 settings. (#10)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.33.0...v0.34.0

## v0.33.0

New `dot ai` commands back up and restore your Claude and Codex settings, and the `ai-tools` module is now called `ai`.

### New
- `dot ai backup` and `dot ai restore` keep versioned snapshots of your Claude and Codex settings, MCP config, agents, hooks, prompts, rules and user skills in the shared backup folder. Auth tokens, caches, logs and session history are left out unless you pass `--include-auth`. `restore` uses the latest snapshot or `--version <id>`. (#9)
- `dot ai export <file>` and `dot ai import <file>` move the same settings as a single archive; `dot ai list` and `dot ai status` show the managed files, detected AI CLIs and backup state. (#9)

### Action needed
- The `ai-tools` module is renamed to `ai`. Use `dot apply --module ai`; `--module ai-tools` now stops with an error. Existing `modules.ai_tools` config is still read. (#9)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.32.0...v0.33.0

## v0.32.0

`dot gdrive-sync` moves to a push-first model with per-workspace settings, safe pulls of Drive-tracked files, and an inbox for new files that appear on Drive.

### New
- Settings now live in `<workspace>/.dotfiles/gdrive-sync/`. `dot gdrive-sync init` creates it, and your existing settings are copied over automatically the first time you run any `dot gdrive-sync` command. Edit `exclude.txt` and `ignore.txt` there to control what syncs.
- `dot gdrive-sync push --propagate=create,update,delete` chooses which kinds of change reach the mirror. By default `push` copies new and changed files and never deletes on the mirror.
- `dot gdrive-sync pull` brings back files listed in `baseline.manifest`, the record of what you pushed, updating your copy when only Drive changed and restoring missing files. When both sides changed, your file is kept and the Drive copy goes to `.sync-conflicts/<time>/from-gdrive/`. `push` runs this pull first and stops if a two-sided edit is unresolved.
- `dot gdrive-sync intake` copies files that are new on Drive into `<workspace>/inbox/gdrive/<time>/` for you to sort, without touching the rest of the workspace (`--strict` compares by checksum). `dot gdrive-sync inbox` shows what is staged; `inbox forget <path>` and `inbox clear` let a file be staged again. Files deleted on Drive are only recorded, never deleted locally.
- `dot gdrive-sync setup --pull-interval=15m` adds a second scheduler for pull and intake; `0` removes it.

### Improved
- Bare `dot gdrive-sync` and `dot gdrive-sync sync` now run `push` and print a hint, instead of pulling and pushing.
- `.gitignore` rules no longer filter what syncs, so ignored binaries and build outputs now reach Drive. Files tracked by git are left to git.

### Action needed
- Commit `.dotfiles/gdrive-sync/baseline.manifest` in your workspace repo so your other machines can `pull` the same files.
- Check `.dotfiles/gdrive-sync/ignore.txt` before your next `push` if you relied on `.gitignore` to keep large files off Drive.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.31.0...v0.32.0

## v0.31.0

`dot gdrive-sync` now finds folders shared with you on Drive and leaves them out, and you can manage that list yourself.

### New
- `dot gdrive-sync shared list`, `add`, `remove` and `clear` manage folders to leave out of sync, for example a folder you own but share with others. Paths can be absolute or relative to the mirror. `dot gdrive-sync status` shows how many shared entries are excluded.

### Improved
- Drive shortcuts to folders shared with you, and anything under `Shared drives/`, are detected and excluded automatically. Folders named `shared/`, `_shared/` or `_external/` are no longer excluded just because of their name.
- `dot gdrive-sync` refuses to run when the mirror folder itself is inside a shared drive, so deletions cannot spread to a team drive.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.30.0...v0.31.0

## v0.30.0

`dot clone reset` turns Google Drive cloning off for good.

### New
- `dot clone reset` removes the clone scheduler, its generated files and the saved remote, path and interval, so a later `dot clone resume` cannot switch it back on. Your rclone credentials and workspace files are left alone. It asks before acting (`--yes` skips the question) and `--dry-run` shows the plan. Run `dot clone setup` to start again.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.29.2...v0.30.0

## v0.29.2

Scheduled syncs on macOS no longer fail with an rsync option error.

### Fixed
- On macOS, every scheduled `dot gdrive-sync` run failed with "rsync: unrecognized option `--info=stats2,progress2'" while manual runs worked, because the scheduler used Apple's older rsync instead of the Homebrew one. The `dot gdrive-sync`, `dot sync` and `dot clone` schedulers now find Homebrew's rsync and rclone first.

### Action needed
- Re-run `dot gdrive-sync setup` (and `dot sync setup` or `dot clone setup` if you use them) so the installed scheduler picks up the fix.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.29.1...v0.29.2

## v0.29.1

Tab completion for `dot` works, and the workspace symlink question in `dot init` is clearer.

### Fixed
- Tab completion did nothing in any shell because `dot` rejected the completion request as an unknown command. It now completes commands and flags.
- The "Symlink target" question in `dot init` now says that it turns your workspace folder itself into a symlink pointing elsewhere, and that leaving it blank keeps a real folder.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.29.0...v0.29.1

## v0.29.0

New `dot gdrive-sync` mirrors your workspace to a Google Drive folder with local rsync, and `dot apply` now sets up shell completion for you.

### New
- `dot gdrive-sync` keeps `~/workspace/work` and a cloud-synced mirror folder (default `~/gdrive-workspace/work`) in step with local rsync; the Drive client does the upload. `push` and `pull` run one direction and `sync` runs both. The workspace wins: `pull` never deletes local files, `push` caps deletions with a `--max-delete` limit, and overwritten files are kept under `.sync-conflicts/<time>/` (list them with `conflicts`).
- `dot gdrive-sync migrate` switches an existing setup from the old `.gdrive`, `inbox/downloads` and `inbox/incoming` symlinks to real folders, after a preflight of sizes, free space and git status. Sync refuses to run until you migrate, and stays paused afterwards until you run `dot gdrive-sync resume`.
- `dot gdrive-sync setup` installs a scheduler (launchd on macOS, a systemd user timer on Linux) that syncs every 5 minutes by default; `pause` and `resume` stop and restart it, and `status` shows the last runs and any conflicts. It runs alongside the `dot sync` scheduler.
- `dot apply` writes zsh, bash and fish completion for `dot` and `dotfiles` and wires it into your shell, so you no longer run `dot completion` by hand.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.28.1...v0.29.0

## v0.28.1

The `dot apps install` picker lists apps in alphabetical order.

### Improved
- The `dot apps install` picker now lists apps alphabetically, so the order and the preselected entries are the same on every run.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.28.0...v0.28.1

## v0.28.0

`dot apps backup` lets you pick which apps to back up and can back up apps that are not in the built-in list.

### New
- `dot apps backup` opens a checkbox picker of your installed apps, with apps you backed up before already ticked. The selection is saved for next time; `--no-save` skips saving, `--select` reopens the picker, and `--yes` or app names skip it. (#7)
- You can back up any installed app by its display name, for example `dot apps backup "Moom Classic"`. `dot` finds the app and backs up its standard preferences, Application Support and container folders. Extra apps in the picker are entered as a comma-separated list, so names with spaces work. (#7)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.27.1...v0.28.0

## v0.27.1

`dot apps install` no longer aborts on apps you installed outside Homebrew.

### Fixed
- If an app from your list was already in `/Applications` (from the App Store or a manual download), `dot apps install` and `dot apply` failed the whole batch with "It seems there is already an App". Those apps are now skipped with a note; use `dot apps install --force` to reinstall them through Homebrew.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.27.0...v0.27.1

## v0.27.0

The macOS app catalog now has a recommended set alongside the minimal defaults.

### New
- `dot apps install --recommended` installs a curated recommended set; `--defaults` still installs the minimal default set. `dot apps list` marks which apps are in each set.

### Improved
- When you have no saved app list, `dot apps install --yes` and the picker's preselection use the recommended set instead of the defaults. Rarely used apps were removed from the catalog.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.26.3...v0.27.0

## v0.26.3

`dot update` works on Linux again, and failing commands now say why.

### Fixed
- On Linux, `dot update` stopped after "Downloading..." without installing anything, because it could not overwrite the running binary. It now swaps the new binary into place safely.
- Any command that failed exited with status 1 and no message. The error is now printed.

### Action needed
- On Linux, `dot update` from an older version still has the bug, so reinstall once with `curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.26.2...v0.26.3

## v0.26.2

`dot init` and `dot reconfigure` ask for workspace repos again when your workspace is already a symlink.

### Fixed
- If you kept an existing `~/workspace` symlink, `dot init` and `dot reconfigure` skipped the question about `work` and `vault` repo remotes. The question is always asked now and defaults to yes.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.26.1...v0.26.2

## v0.26.1

npm 11 no longer prints config warnings on every run.

### Fixed
- npm 11 warned about `virtual-store-dir`, `store-dir` and `cache-dir` on every command because `dot` wrote these pnpm-only settings to `~/.npmrc`. They now live in `~/.config/pnpm/npmrc`, which only pnpm reads, and `dot apply` removes the old lines from `~/.npmrc` (deleting the file if nothing else is left).

### Action needed
- Run `dot apply` once and open a new shell so the old `~/.npmrc` lines are cleaned up and pnpm picks up its new config.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.26.0...v0.26.1

## v0.26.0

Report commands share one consistent layout, and `dot clone setup --dry-run` now sees your real rclone setup.

### Improved
- `dot status`, `dot config`, `dot check`, `dot diff`, `dot doctor`, `dot clean`, `dot preflight`, `dot drive-exclude`, `dot apps`, `dot profile list` and the clone and sync status views now use the same headers, sections and status markers, so results line up the same way everywhere. No information was removed.

### Fixed
- `dot clone setup --dry-run` no longer shows an empty rclone version or asks to configure a Google Drive remote that already exists.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.25.3...v0.26.0

## v0.25.3

Fixes wrapped labels in the `dot config` sync section.

### Fixed
- The rclone and rsync labels in `dot config` no longer wrap onto a second line.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.25.2...v0.25.3

## v0.25.2

`dot config` now shows your full saved setup in one view.

### Improved
- `dot config` adds the prompt style, app install and backup lists, backup folder with the last backup time, secrets settings, and the rclone and rsync sync settings.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.25.1...v0.25.2

## v0.25.1

You can now view or change the backup folder without re-running `dot init`.

### New
- `dot profile root` shows the backup folder in use; `dot profile root <path>` saves a new one, `--detect` finds and saves your Google Drive folder, and `--reset` goes back to automatic detection.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.25.0...v0.25.1

## v0.25.0

You can now choose between a minimal and a rich shell prompt.

### New
- `dot init` and `dot reconfigure` ask for a prompt style: `minimal` (short path, branch, dirty marker) or `rich` (time, user, host, path, git status, language versions). The choice is saved as `terminal.prompt_style` and applied by `dot apply`. The full profile defaults to `rich`; minimal and server default to `minimal`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.24.0...v0.25.0

## v0.24.0

`dot` can now install your macOS apps from a catalog, back up and restore their settings, and keep versioned snapshots of your `dot` profile.

### New
- `dot apps` manages macOS apps through Homebrew casks: `list` shows a catalog of about 60 apps, `install` opens a checkbox picker (or takes app names), `status` shows what is installed and backed up, and `backup`/`restore` copy app settings (preferences, Application Support, containers) for 21 supported apps to a backup folder. Caches and logs are skipped.
- `dot profile backup`, `restore`, `list` and `prune` keep versioned snapshots of your `dot` configuration and app lists, with `~/.ssh/age_key*` optional. `restore` uses the latest snapshot unless you pass `--version`; `prune --keep N` keeps the newest N.
- `dot init` asks which apps to install, which apps to back up, and where the backup folder lives, and detects a Google Drive folder for it automatically. `dot apps` and `dot profile` share that folder.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.23.0...v0.24.0

## v0.23.0

`dot ws init` clones your configured workspace repos in one step, including their submodules.

### New
- `dot ws init` clones each configured repo (such as `work` and `vault`) into `<workspace.path>/<name>` with its submodules. Repos that already have content are skipped; `--force` re-clones them after asking. It checks `gh` authentication before cloning so private repos no longer fail silently on a fresh machine, and keeps an existing `.gdrive` symlink in place.

### Improved
- `dot apply` now clones workspace repos the same way as `dot ws init`, including submodules, and `--dry-run` lists each repo it would clone.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.22.0...v0.23.0

## v0.22.0

`dot ws` creates, moves and removes folders in both workspaces at once so their structure stays in step.

### New
- `dot ws mkdir`, `dot ws mv` and `dot ws rm` act on the same relative path in `~/workspace/work/` and `~/gdrive-workspace/work/`. `mv` refuses an existing destination and `rm` needs `-r` for a non-empty folder.
- `dot ws audit` reports folders that exist on only one side, and `dot ws reconcile` resolves each one interactively (copy, delete or skip). With `--yes`, reconcile only copies and never deletes.
- Top-level shortcuts: `dot ws-mkdir`, `dot ws-mv`, `dot ws-rm`, `dot ws-audit`, `dot ws-reconcile`. All of them honor `--dry-run`.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.21.1...v0.22.0

## v0.21.1

`dot check` reports installed packages correctly again, and `dot apply` stops creating the `.vault` and `.work` cross-links.

### Improved
- `dot apply` no longer creates the `work/.vault` and `vault/.work` symlinks; both folders are reachable directly under `~/workspace/`.
- The global gitignore now ignores `.claude/settings.local.json`.
- `dot check` ends with a count of satisfied and pending items.

### Fixed
- `dot check`, `dot diff` and `dot status` showed every Homebrew package as pending even when it was installed.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.21.0...v0.21.1

## v0.21.0

`dot apply` can clone your `work` and `vault` repos on a new machine, signing in to GitHub first when needed.

### New
- `dot init` optionally asks for git remote URLs for the `work` and `vault` folders, and `dot apply` clones them before creating symlinks. If `gh` is not signed in, it offers an interactive login with the scopes needed for private repos. A failed clone does not stop the rest of apply.
- These repos are included in `dot config export` and `dot init --from`.

### Improved
- `dot config` has a Workspace section showing the path, Google Drive folder, Drive symlink and configured repos.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.20.0...v0.21.0

## v0.20.0

The workspace module supports two workspaces side by side: a local `~/workspace` and a Google Drive copy linked as `~/gdrive-workspace`.

### New
- New `gdrive_symlink` workspace setting (for example `~/gdrive-workspace`), asked for by `dot init`. `dot apply` links it to your Google Drive folder, adds `work/.gdrive`, and links `work/inbox/downloads` and `work/inbox/incoming` to their Drive counterparts.
- Shell helpers for the Drive workspace: `$GDRIVE_WORKSPACE`, `$GDRIVE_WORK`, a `gwork` alias, and `ws`, which jumps between the matching folders in the two workspaces.

### Improved
- The suggested workspace path is now `~/workspace` instead of `~/ai-workspace`.

### Fixed
- Workspace paths written with `~` in the config were not expanded, so symlinks pointed at a literal `~` path.

### Action needed
- `dot apply` no longer creates `~/.brain`, and the `brain` alias is gone; use the `vault` alias.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.19.1...v0.20.0

## v0.19.1

The install script no longer fails when your GitHub credentials are stale.

### Fixed
- The install script failed with an expired or invalid `GITHUB_TOKEN` (HTTP 401) or old credentials in `~/.netrc`. It now ignores `.netrc` and falls back to an unauthenticated request, then to the release redirect URL.
- The install script could exit early on a failed `curl` inside a pipeline without saying why.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.19.0...v0.19.1

## v0.19.0

Move your setup to a new machine with `dot config export` and `dot init --from`, and install on a fresh Mac or Ubuntu server with one command.

### New
- `dot config export [path]` writes your settings to a portable YAML file, and `dot init --from <file>` imports it on another machine with your identity pre-filled.
- The install script prepares a fresh Mac: it installs Homebrew (and the Xcode command line tools), adds it to your shell PATH, skips the download when you already have the latest version, and uses `GITHUB_TOKEN` to avoid API rate limits. On Ubuntu it installs Linuxbrew and its build prerequisites.

### Improved
- The `minimal` profile installs rsync and the `full` profile installs rclone.

### Fixed
- `dot secrets restore` mishandled paths starting with `~/`.
- `dot secrets init` and `dot secrets restore` now check for the `age` binary first and say so, instead of failing partway.
- `dot init --from` with an empty file now fails with a clear error.
- `dot clone` help text and the rclone skip file named the wrong command.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.18.0...v0.19.0

## v0.18.0

Google Drive sync moves to `dot clone`, `dot sync` now syncs with a remote server over rsync, and `dot clean` removes workspace junk.

### New
- `dot clean [path]` removes `node_modules`, caches and virtual environments from your workspace. `--all` also includes `dist/`, `build/`, `out/` and `target/`. Folders under `_sys/` are protected.
- `dot sync` syncs binary files with a remote server over rsync and SSH (pull, then push). `dot sync setup` installs rsync, configures SSH, installs the list of synced file extensions and the scheduler. `dot status` shows its state.

### Improved
- `dot drive-exclude` recognizes more heavy directory patterns (22, up from 14).

### Action needed
- The rclone Google Drive sync from earlier releases is now `dot clone` (`dot clone pull`, `push`, `all`, `mount`, `setup` and so on). Replace `dot sync` with `dot clone` in your scripts and habits.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.17.0...v0.18.0

## v0.17.0

`dot status` shows your whole environment on one screen.

### New
- `dot status` combines system, user, module, secrets, sync and workspace information in one view. A section that fails to load is reported without hiding the rest.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.16.0...v0.17.0

## v0.16.0

`dot secrets` remembers where your last backup went and can create the secrets file for you.

### New
- `dot secrets init --scaffold` creates an empty `~/.config/shell/90-secrets.sh` with `0600` permissions if it is missing.
- `dot secrets backup` records the backup path, time and file count, and `dot secrets status` shows where your most recent backup went and how long ago.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.15.0...v0.16.0

## v0.15.0

`dot sync` is rebuilt around one-way pull and push commands, `dot` shows a welcome screen, and `dot <project>` no longer opens a workspace by itself.

### New
- Running `dot` with no command shows your configuration summary and common commands; `dot usecase` walks through nine workflows, from first-time setup to GPU servers.
- `dot sync pull` downloads newer files, `dot sync push` uploads newer files, `dot sync all` does both, and `dot sync mount` mounts the remote as a live FUSE filesystem (`--daemon`, `--unmount`). Plain `dot sync` now only pulls.
- Files that fail with permission errors are skipped on later syncs; `dot sync skip` lists them and `dot sync skip clear` retries all of them.
- `dot sync --verbose` (`-V`) streams rclone progress to the terminal.
- With the `full` profile, the `node` module installs the `qmd` markdown search tool when npm is available.

### Improved
- `dot sync` copies newer files instead of running rclone bisync, so it no longer needs a baseline and works on Drive folders with shared files and Google Docs. Shared-with-me files are skipped at the source.
- `dot init` and `dot reconfigure` pre-fill answers you can edit, detected from git config, `gh`, your timezone and existing workspace or Google Drive folders. `dot config` output is easier to scan.
- `dot upgrade` is now `dot update` (`upgrade` still works). It retries failed downloads and checks that the new binary runs before replacing the current one.
- `dot sync status` shows whether the remote is mounted and the last transfer stats.

### Fixed
- `dot update` treated any version other than the latest release as outdated, so a newer local build was offered a downgrade.

### Action needed
- `dot <project>` no longer opens a workspace; use `dot open <project>`. This stops a typo such as `dot aply` from registering a project.
- `dot sync` now pulls only. Run `dot sync all` for the old two-way behavior. `dot sync --resync` and `dot sync reset` are removed because no baseline is needed.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.14.0...v0.15.0

## v0.14.0

`dot sync setup` now completes on Google Drive workspaces with shared files, and `dot sync reset` rebuilds the baseline on demand.

### New
- `dot sync reset` recreates the sync baseline.

### Fixed
- `dot sync setup` aborted on Google Drive workspaces that contain shared files (`insufficientFilePermissions`). It now builds the baseline from file listings instead of a full resync.
- Long syncs failed with Google Drive `rateLimitExceeded` errors; `dot sync` now sends fewer requests per second and retries transient errors.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.13.0...v0.14.0

## v0.13.0

`dot sync` is faster on large workspaces and no longer stops on broken Drive shortcuts.

### Improved
- `dot sync` lists large workspaces with far fewer Drive API calls, which speeds up syncs of tens of thousands of files.

### Fixed
- `dot sync` and scheduled syncs aborted on dangling Google Drive shortcuts; those shortcuts are now skipped.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.12.0...v0.13.0

## v0.12.0

`dot sync --resync` creates the first sync baseline without rerunning setup.

### New
- `dot sync --resync` builds the initial sync baseline, for when `dot sync setup` timed out but the remote is reachable.

### Fixed
- `dot sync` failed because rclone rejects the logging flags it was given.
- When no baseline exists, `dot sync` now says so and points to `dot sync setup` instead of failing with an rclone error.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.11.0...v0.12.0

## v0.11.0

Easier Google Drive connection for `dot sync`, and no more hangs when Drive or your login is stuck.

### New
- `dot sync connect` sets up a new Google Drive remote and `dot sync reconnect` refreshes expired authentication. Error messages now point to the right `dot sync` command instead of raw rclone commands.

### Fixed
- `dot sync` hung indefinitely when Google Drive for Desktop was frozen.
- `dot sync setup` hung on an expired login; the remote check now times out after 15 seconds, and setup skips the first sync with instructions when the remote is unreachable.
- A failed `dot sync` command no longer prints the full help text after the error.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.10.0...v0.11.0

## v0.10.0

`dot sync` keeps your workspace in sync with Google Drive using rclone, with an optional background scheduler.

### New
- `dot sync setup` installs rclone, configures the Google Drive remote, installs a filter that skips `node_modules`, build caches, `.venv` and `.git`, and schedules automatic syncs (launchd on macOS, systemd on Linux).
- `dot sync` runs a two-way sync now. `dot sync status` shows the scheduler state and last sync time, `dot sync log` shows recent log lines, and `dot sync pause`/`dot sync resume` control the scheduler.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.9.0...v0.10.0

## v0.9.0

`dot drive-exclude` now works on a workspace that is a symlink into Google Drive, and can exclude folders you name.

### New
- `dot drive-exclude add <path>...` excludes specific directories from Google Drive sync.

### Fixed
- `dot drive-exclude scan` found nothing when the workspace was a symlink to the Google Drive mount.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.8.0...v0.9.0

## v0.8.0

`dot drive-exclude` keeps `node_modules`, build caches and Python venvs out of Google Drive sync on macOS.

### New
- `dot drive-exclude scan` finds heavy directories in your workspace, `dot drive-exclude apply` marks them as excluded from Google Drive sync, and `dot drive-exclude status` shows what is excluded. `--dry-run` and `--yes` are supported.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.7.0...v0.8.0

## v0.7.0

`dot config` shows your current configuration in one view.

### New
- `dot config` prints the active profile, system info, your settings, enabled and disabled modules, and installed packages.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.6.0...v0.7.0

## v0.6.0

A new `node` module keeps pnpm stores out of cloud-synced folders.

### New
- The `node` module writes `~/.npmrc` so pnpm's store, virtual store and cache live under `~/.local/share/pnpm/` and `~/.cache/pnpm/`, keeping heavy `node_modules` data out of Google Drive project folders.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.5.0...v0.6.0

## v0.5.0

`dot init` finds your existing SSH and age keys, and `dot apply` no longer replaces an existing workspace symlink.

### Improved
- `dot init` lists the SSH key pairs and age key files it finds in `~/.ssh/` so you can pick one instead of typing the path, and offers to keep an existing workspace symlink.

### Fixed
- `dot apply` no longer replaces an existing workspace symlink (for example `~/ai-workspace` pointing at Google Drive) with a different target. It now creates a symlink only when the path does not exist and you set an explicit `symlink` target in the config.

### Action needed
- If you relied on `dot apply` creating the workspace symlink from the `gdrive` setting, set the `symlink` field in your workspace config instead.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.4.0...v0.5.0

## v0.4.0

Adds `dot`, a short name for `dotfiles`, and a tmux workspace manager that opens multi-pane project layouts with one command.

### New
- `dot` is installed as a shortcut for `dotfiles`; both names run the same commands. (#2)
- `dot open <project>` starts or resumes a tmux workspace; `dot stop` ends it, `dot list` (or `ls`) shows projects and running sessions, and `dot register`/`dot unregister` manage the project list. Unknown projects are registered after you confirm. (#2)
- Three layouts (`dev`, `claude`, `monitor`; see `dot layouts`) and five themes (default, dracula, nord, catppuccin, tokyo-night), applied per session so they do not leak into other workspaces. (#2)
- Panes fall back to the tools you have installed (for example btop, then htop, then top). `dot doctor` shows which tools are installed. (#2)
- `dot <project>` works as a shortcut for `dot open <project>`. (#2)

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.3.0...v0.4.0

## v0.3.0

Richer tmux setup with new shortcuts and helpers, and package installation that works on machines without Homebrew.

### New
- tmux keybindings for swapping windows, resizing panes and better copy mode, activity monitoring, and a cheatsheet popup on `C-a /`.
- Shell aliases `ta`, `ts`, `tl`, `tk`, `td` and a `t` function that attaches to a tmux session or creates it.

### Fixed
- `dotfiles apply` installs Homebrew when it is missing. Before, the packages module reported success and tools such as starship, eza and bat were never installed.
- `dotfiles apply` no longer fails when `brew install` exits with a non-fatal post-install warning even though every package was installed.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.2.0...v0.3.0

## v0.2.0

Adds a `server` profile for GPU and DGX machines, an environment check, and self-upgrade from GitHub releases.

### New
- `dotfiles preflight` checks the OS, shell, git, Homebrew, GPU, CUDA and network, then writes a starting `config.yaml` from what it finds. `--check-only` reports without writing; `--force` overwrites an existing config.
- `server` profile for Linux GPU machines: adds tmux, AI tools and conda, skips workspace, fonts, GPG and secrets. It is suggested automatically when an NVIDIA GPU is detected.
- On GPU machines the shell sets `CUDA_HOME`, `PATH` and `LD_LIBRARY_PATH`, and adds `gpustat`/`gpuwatch` aliases.
- `dotfiles upgrade` downloads and installs the latest release binary; `--check` only reports whether a newer version exists.
- `--home <dir>` sets up another user's home directory, for admins provisioning several accounts.

### Improved
- `dotfiles init` and `dotfiles apply` walk through each section (identity, SSH, workspace, AI tools, terminal, fonts) for review, and `--yes` skips all prompts for unattended runs.

### Fixed
- On Linux, making zsh the default shell no longer relies on the macOS-only `dscl` command.

### Action needed
- `dotfiles update` is replaced by `dotfiles upgrade`, which installs the release binary instead of pulling a git checkout.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/compare/v0.1.0...v0.2.0

## v0.1.0

First release of `dotfiles`, a single Go binary that sets up your shell, git, SSH, terminal and tools on macOS and Ubuntu.

### New
- `dotfiles init` asks for your name, email, GitHub user and workspace path, then `dotfiles apply` writes the configuration. `dotfiles check` and `dotfiles diff` show what would change, and `--dry-run` previews any command without touching files.
- Two profiles, `minimal` and `full`, pick which modules run: packages (Homebrew), shell, git, SSH, terminal, tmux, workspace, AI tools, fonts, conda, GPG and secrets. `--module` runs only the ones you name.
- Existing files are backed up before they are overwritten, and unchanged files are left alone.
- `dotfiles secrets` (`init`, `backup`, `restore`, `list`, `status`) keeps encrypted secrets outside the repo using age with your SSH key.
- `dotfiles reconfigure` re-runs the setup questions, and `dotfiles update` pulls the latest dotfiles and re-applies them.
- Zsh tab completion with menu selection and case-insensitive matching.

**Full changelog**: https://github.com/STAIxBWLB/dotfiles/commits/v0.1.0
