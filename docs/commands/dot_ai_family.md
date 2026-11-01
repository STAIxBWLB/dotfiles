## dot ai family

Install agent-hub family tools side by side, one directory per version

### Synopsis

Install ahub, amem, vmem, acon, ares side by side.

Each version gets its own npm prefix under ~/.local/share/dotfiles/tools/<id>/<version>/.
A 'current' link names the active version and 'previous' the one before it;
~/.local/bin/<bin> points through 'current'. Activation is one rename: new
sessions start the new version, running processes keep the files they started
with and are listed, and nothing is stopped. Versions other than current and
previous are removed once no running process uses them. 'dot ai rollback <id>'
makes previous current again.

### Options

```
  -h, --help   help for family
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

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai family install](dot_ai_family_install.md)	 - Install a version of a family tool and make it current
* [dot ai family status](dot_ai_family_status.md)	 - Show each family tool's current and previous versions

