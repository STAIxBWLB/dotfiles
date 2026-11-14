## dot init

Interactive setup for dot

### Synopsis

Collect user preferences and save them to the dot state file.

Use --from to import settings from another machine's exported config:
  dot init --from ~/workspace/secrets/dotfiles-config.yaml

Use --check-only to scan the environment without writing anything, or
--detect to write a config generated from the detected system without
prompts (--force overwrites an existing one).

```
dot init [flags]
```

### Options

```
      --check-only       Only run environment checks, don't write config
      --detect           Write a config generated from the detected system, without prompts
      --force            With --detect, overwrite an existing config file
      --from string      Import settings from an exported config file (identity & preferences as defaults)
  -h, --help             help for init
      --profile string   Profile name (minimal, full, server)
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

