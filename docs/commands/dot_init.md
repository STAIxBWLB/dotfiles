## dot init

Interactive setup for dot

### Synopsis

Collect user preferences and save them to the dot state file.

Use --from to import settings from another machine's exported config. Use
--check-only to scan the environment without writing anything, or
--detect to write a config generated from the detected system without
prompts (--force overwrites an existing one).

```
dot init [flags]
```

### Examples

```
  dot init
  dot init --from ~/workspace/secrets/dotfiles-config.yaml
  dot init --detect
```

### Options

```
      --check-only[=true|false]   only run environment checks, don't write config
      --detect[=true|false]       write a config generated from the detected system, without prompts
      --force[=true|false]        with --detect, overwrite an existing config file
      --from string               import settings from an exported config file (identity & preferences as defaults)
  -h, --help[=true|false]         help for init
      --profile string            profile name (minimal, full, server)
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool

