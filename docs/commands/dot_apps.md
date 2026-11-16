## dot apps

Install macOS apps and back up their settings

### Synopsis

Manage macOS cask applications and their user settings.

Subcommands:
  list     Show the embedded cask catalog (groups, defaults).
  install  Install the selected casks (uses saved state, brew install --cask).
  status   Report install + backup presence for each tracked app.
  backup   Copy app settings to the host-scoped backup archive.
  restore  Copy app settings back from the archive.

### Options

```
  -h, --help   help for apps
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --verbose         show library and debug logs on stderr
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot apps backup](dot_apps_backup.md)	 - Snapshot only macOS app settings
* [dot apps install](dot_apps_install.md)	 - Install macOS cask apps
* [dot apps list](dot_apps_list.md)	 - Show the cask catalog (groups + defaults)
* [dot apps restore](dot_apps_restore.md)	 - Restore only macOS app settings
* [dot apps status](dot_apps_status.md)	 - Show install + backup presence for tracked apps

