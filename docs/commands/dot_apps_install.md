## dot apps install

Install macOS cask apps (interactive by default; args skip the picker)

### Synopsis

Install macOS cask applications.

Modes:
  - positional args       : install exactly those tokens.
  - --defaults            : install the catalog's default set.
  - --recommended         : install the catalog's recommended set.
  - --all                 : install every cask in the catalog.
  - --select              : open the checkbox picker even when state is set.
  - no args + interactive : open the checkbox picker, preselected from saved state.
  - no args + --yes       : use saved state (falls back to catalog recommended).

Casks whose .app already exists under /Applications (e.g. installed via the
App Store or downloaded directly) are skipped by default. Pass --force to
reinstall them over the existing bundle.

After an interactive run, the updated selection can be saved back to the user
state file so subsequent 'dot apply' runs honor it.

```
dot apps install [token...] [flags]
```

### Options

```
      --all           Install every app in the catalog
      --defaults      Install the catalog's default set regardless of saved state
      --force         Reinstall even when the .app already exists under /Applications
  -h, --help          help for install
      --no-save       Do not persist the interactive selection back to state
      --recommended   Install the catalog's recommended set regardless of saved state
      --select        Force the interactive picker even when state has a list
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot apps](dot_apps.md)	 - macOS app install and settings backup/restore

