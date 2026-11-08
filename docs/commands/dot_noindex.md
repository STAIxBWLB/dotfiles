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

### Options

```
  -h, --help      help for noindex
  -v, --verbose   List every directory marked
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot noindex setup](dot_noindex_setup.md)	 - Install the periodic noindex sweep as a LaunchAgent
* [dot noindex uninstall](dot_noindex_uninstall.md)	 - Remove the periodic noindex sweep

