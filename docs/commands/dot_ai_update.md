## dot ai update

Update explicitly selected agent CLIs and development tools

### Synopsis

Update only the saved nine-agent/four-addon allowlist.

Run 'dot ai setup' first. Native providers retain installation ownership.
Heavy work is serialized by the host resource guard. Unavailable metadata,
unsupported adapters, resource pressure, authentication and trust remain
explicitly pending; no blanket plugin, marketplace, or skill update runs.

```
dot ai update [flags]
```

### Examples

```
  dot ai update --check
  dot ai update --tool claude,codex
```

### Options

```
      --check[=true|false]   inspect installed and available versions without mutation
  -h, --help[=true|false]    help for update
      --json[=true|false]    emit machine-readable JSON
      --tool strings         limit to saved selections (claude,codex,kimi,qwen,grok,opencode,gencode,pi,antigravity,ripwire,ocr,gsd,ponytail)
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

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai update schedule](dot_ai_update_schedule.md)	 - Manage opt-in Sunday 04:00 stable maintenance (macOS)

