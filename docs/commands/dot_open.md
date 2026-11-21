## dot open

Launch or resume a tmux workspace

### Synopsis

Launch a new tmux workspace for a project, or resume an existing session.
If the project is not registered, you'll be prompted to register it.

```
dot open <project> [flags]
```

### Examples

```
  dot open myproject
  dot open myproject --layout claude
```

### Options

```
  -h, --help[=true|false]               help for open
      --install-optional[=true|false]   also install optional tools (lazygit, btop, yazi, eza)
      --layout string                   layout to use (dev, claude, monitor, ahub, ahub-compact); a running session keeps its layout
      --theme string                    theme to use (default, dracula, nord, catppuccin, tokyo-night)
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

