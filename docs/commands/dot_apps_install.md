## dot apps install

Install macOS cask apps

### Synopsis

Install macOS cask applications.

Modes:
  - positional args       : install exactly those tokens.
  - --defaults            : install the catalog's default set.
  - --recommended         : install the catalog's recommended set.
  - --all                 : install every cask in the catalog.
  - --select              : open the checkbox picker even when state is set.
  - no args + interactive : open the checkbox picker, preselected from saved
                            state.
  - no args + --yes       : use saved state (falls back to catalog
                            recommended).

Casks whose .app already exists under /Applications (e.g. installed via the
App Store or downloaded directly) are skipped by default. Pass --force to
reinstall them over the existing bundle.

After an interactive run, the updated selection can be saved back to the user
state file so subsequent 'dot apply' runs honor it.

```
dot apps install [token...] [flags]
```

### Examples

```
  dot apps install --recommended
  dot apps install raycast
```

### Options

```
      --all           install every app in the catalog
      --defaults      install the catalog's default set regardless of saved state
      --force         reinstall even when the .app already exists under /Applications
  -h, --help          help for install
      --no-save       do not persist the interactive selection back to state
      --recommended   install the catalog's recommended set regardless of saved state
      --select        force the interactive picker even when state has a list
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

* [dot apps](dot_apps.md)	 - Install macOS apps and back up their settings

