## dot watchdog uninstall

Remove the watchdog reaper agent, WARP heal daemon, monit supervision, and Beszel agent plist (macOS)

### Synopsis

Unload and remove the reaper LaunchAgent, the monit agent and monitrc,
the Beszel agent LaunchAgent, and, when installed, the root WARP heal
LaunchDaemon. The secrets-managed Beszel env file
(~/.config/beszel/agent.env) is never removed. Restoring the power settings
saved by setup --headless, removing the root Screen Sharing heal helper and
its sudoers grant, and removing the state directory and logs are
interactive-only prompts that default to No; --yes never auto-confirms them.

```
dot watchdog uninstall [flags]
```

### Options

```
  -h, --help   help for uninstall
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot watchdog](dot_watchdog.md)	 - Reap runaway processes and alert on host health

