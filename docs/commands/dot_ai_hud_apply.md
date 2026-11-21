## dot ai hud apply

Apply dot-native HUD settings to Claude Code and Codex

```
dot ai hud apply [flags]
```

### Examples

```
  dot ai hud apply
  dot ai hud apply --tool claude --persist
```

### Options

```
      --force[=true|false]     take over a statusLine currently owned by another tool
  -h, --help[=true|false]      help for apply
      --persist[=true|false]   persist modules.ai.hud=true for future dot apply runs
      --tool string            comma-separated tool IDs (claude,codex)
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

* [dot ai hud](dot_ai_hud.md)	 - Manage dot-native Claude Code and Codex HUD status lines

