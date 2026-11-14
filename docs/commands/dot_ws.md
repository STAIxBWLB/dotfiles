## dot ws

Dual-workspace (work + cloud mirror) folder operations

### Synopsis

Operate on both the workspace root and the sync mirror simultaneously.

Subcommands keep the two trees in structural sync:
  init       Clone configured workspace repos (recursive)
  mkdir      Create a folder on both sides
  mv         Rename/move on both sides
  rm         Remove on both sides (use --recursive for non-empty)
  audit      Report structural mismatches (read-only)
  reconcile  Interactively resolve mismatches

### Options

```
  -h, --help   help for ws
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot ws audit](dot_ws_audit.md)	 - Report structural mismatches between workspaces
* [dot ws init](dot_ws_init.md)	 - Clone configured workspace repos recursively
* [dot ws mkdir](dot_ws_mkdir.md)	 - Create a directory on both workspaces
* [dot ws mv](dot_ws_mv.md)	 - Rename/move a directory on both workspaces
* [dot ws reconcile](dot_ws_reconcile.md)	 - Interactively resolve workspace mismatches
* [dot ws rm](dot_ws_rm.md)	 - Remove a directory from both workspaces

