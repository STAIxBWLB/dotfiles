## dot

User environment & workspace management tool

### Synopsis

dotfiles: Declarative user environment configuration with modular profiles.

Run without arguments to see a getting-started guide.
Run 'dot usecase' for detailed workflow examples.
Also available as 'dotfiles' for back-compat.

```
dot [flags]
```

### Options

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
  -h, --help            help for dot
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot admit](dot_admit.md)	 - Run one heavy job per repo behind the host-pressure gate
* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot apply](dot_apply.md)	 - Apply dot configuration
* [dot apps](dot_apps.md)	 - macOS app install and settings backup/restore
* [dot backup](dot_backup.md)	 - One-stop interactive backup (profile, apps, AI, secrets)
* [dot clean](dot_clean.md)	 - Remove junk directories (node_modules, caches, venvs) from workspace
* [dot completion](dot_completion.md)	 - Generate the autocompletion script for the specified shell
* [dot config](dot_config.md)	 - Show current configuration
* [dot container](dot_container.md)	 - Shared container command (apple/container on macOS, docker or podman on Linux)
* [dot diff](dot_diff.md)	 - Show pending changes without applying
* [dot guard](dot_guard.md)	 - Claude Code safety hooks (careful warnings + freeze boundary)
* [dot hub](dot_hub.md)	 - Open a registered project's agent-hub console, status or dashboard, or its Git or Files window
* [dot init](dot_init.md)	 - Interactive setup for dot
* [dot layouts](dot_layouts.md)	 - List available workspace layouts
* [dot list](dot_list.md)	 - List registered projects and active tmux sessions
* [dot noindex](dot_noindex.md)	 - Keep Spotlight out of build and cache directories
* [dot open](dot_open.md)	 - Launch or resume a tmux workspace
* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH
* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state (config, app lists, secrets)
* [dot reconfigure](dot_reconfigure.md)	 - Re-run init prompts with current values as defaults
* [dot register](dot_register.md)	 - Register a project for workspace management
* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host
* [dot restore](dot_restore.md)	 - One-stop interactive restore (profile, apps, AI, secrets)
* [dot rollback](dot_rollback.md)	 - Activate the previous standalone dot binary
* [dot secrets](dot_secrets.md)	 - Manage encrypted secrets
* [dot status](dot_status.md)	 - Show full environment status at a glance
* [dot stop](dot_stop.md)	 - Stop a tmux workspace session
* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot tunnel](dot_tunnel.md)	 - Manage Cloudflare Tunnel SSH access for this Mac
* [dot unregister](dot_unregister.md)	 - Remove a registered project
* [dot update](dot_update.md)	 - Update dot binary to the latest or a selected version
* [dot usecase](dot_usecase.md)	 - Show detailed use cases and workflows
* [dot version](dot_version.md)	 - Print version information
* [dot watchdog](dot_watchdog.md)	 - Reap runaway processes and alert on host health
* [dot ws](dot_ws.md)	 - Dual-workspace (work + cloud mirror) folder operations

