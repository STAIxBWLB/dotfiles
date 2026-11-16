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
  -h, --help   help for vault
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

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore

