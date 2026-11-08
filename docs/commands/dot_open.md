## dot open

Launch or resume a tmux workspace

### Synopsis

Launch a new tmux workspace for a project, or resume an existing session.
If the project is not registered, you'll be prompted to register it.

```
dot open <project> [flags]
```

### Options

```
  -h, --help               help for open
      --install-optional   Also install optional tools (lazygit, btop, yazi, eza)
      --layout string      Layout to use (dev, claude, monitor, ahub, ahub-compact); a running session keeps its layout
      --theme string       Theme to use (default, dracula, nord, catppuccin, tokyo-night)
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool

