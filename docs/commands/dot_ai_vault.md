## dot ai vault

Register vmem as the agents' vault server

### Synopsis

Register vmem (@staix/vault-mem) as the agents' vault server for Kiro and
GitHub Copilot, and print the 'vmem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi, jcode and Antigravity, which vmem registers itself.

dot runs no vault process and installs vmem only on request: install the exact
version yourself or with 'dot ai family install vmem', then run
'dot ai mcp install vmem'.

```
dot ai vault [flags]
```

### Examples

```
  dot ai vault --help
```

### Options

```
  -h, --help[=true|false]   help for vault
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

