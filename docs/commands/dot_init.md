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
      --check-only    Only run environment checks, don't write config
      --detect        Write a config generated from the detected system, without prompts
      --force         With --detect, overwrite an existing config file
      --from string   Import settings from an exported config file (identity & preferences as defaults)
  -h, --help          help for init
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool

