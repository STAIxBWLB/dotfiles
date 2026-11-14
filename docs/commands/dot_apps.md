## dot apps

macOS app install and settings backup/restore

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
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot apps backup](dot_apps_backup.md)	 - Advanced: snapshot only macOS app settings (dot backup is the front door)
* [dot apps install](dot_apps_install.md)	 - Install macOS cask apps (interactive by default; args skip the picker)
* [dot apps list](dot_apps_list.md)	 - Show the cask catalog (groups + defaults)
* [dot apps restore](dot_apps_restore.md)	 - Advanced: restore only macOS app settings (dot restore is the front door)
* [dot apps status](dot_apps_status.md)	 - Show install + backup presence for tracked apps

