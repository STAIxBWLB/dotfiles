## dot noindex

Keep Spotlight out of build and cache directories

### Synopsis

Drop a .metadata_never_index marker into regenerable directories so
macOS Spotlight skips them and everything under them.

With no arguments this sweeps the default roots: project trees
(~/workspace, ~/Sites, ...) are walked and every node_modules, .venv,
.next, target, ... inside them gets its own marker, while tool and cache
trees (~/.local, ~/.npm, ~/.cursor, ...) get a single marker at the top.
Pass paths to walk those instead.

build/ and out/ are left alone even though dot clean calls them junk:
that is where finished deliverables land, and they should stay findable.

The marker only stops future indexing. Anything already in the Spotlight
store stays there until a full reindex (sudo mdutil -E /).

Interactive shells stamp ./node_modules right after npm/pnpm/yarn/bun
finish, so this command is the backstop for everything else.

```
dot noindex [path...] [flags]
```

### Examples

```
  dot noindex
  dot noindex ~/workspace --verbose
```

### Options

```
  -h, --help[=true|false]      help for noindex
  -v, --verbose[=true|false]   list every directory marked
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot noindex setup](dot_noindex_setup.md)	 - Install the periodic noindex sweep as a LaunchAgent
* [dot noindex uninstall](dot_noindex_uninstall.md)	 - Remove the periodic noindex sweep

