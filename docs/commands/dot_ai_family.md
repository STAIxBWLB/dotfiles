## dot ai family

Install agent-hub family tools side by side, one directory per version

### Synopsis

Install ahub, amem, vmem, acon, ares side by side.

Each version gets its own npm prefix under ~/.local/share/dotfiles/tools/<id>/<version>/.
A 'current' link names the active version and 'previous' the one before it;
~/.local/bin/<bin> points through 'current'. Activation is one rename: new
sessions start the new version, running processes keep the files they started
with and are listed, and nothing is stopped. Versions other than current and
previous are removed once no running process uses them. 'dot ai family rollback <id>'
makes previous current again.

For ahub this is the global CLI only: 'ahub upgrade' still runs each project's
daemon from ~/.agenthub/releases. After ahub activates, run the printed
'ahub setup' to move the shared Claude plugin to the same version.

### Options

```
  -h, --help   help for family
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai family install](dot_ai_family_install.md)	 - Install family tools and make each one current
* [dot ai family rollback](dot_ai_family_rollback.md)	 - Make a family tool's previous version current again
* [dot ai family status](dot_ai_family_status.md)	 - Show each family tool's current and previous versions
* [dot ai family update](dot_ai_family_update.md)	 - Move installed family tools to the registry's latest version

