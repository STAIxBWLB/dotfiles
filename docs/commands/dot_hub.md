## dot hub

Open a project's agent-hub console or window

### Synopsis

Open agent-hub (ahub) for a registered project, at its registered root.

  console (default)  Outside tmux, runs 'ahub --project <root> console' in this
                     terminal. Inside that project's workspace session, focuses
                     its AHUB pane, or adds one 'hub' window to a session
                     without one. Elsewhere in tmux it prints how to switch.
  status             Runs 'ahub --project <root> status' once.
  ui                 Runs 'ahub --project <root> ui' (the local dashboard).
  git, files         Inside that project's workspace session only: focuses its
                     'git' or 'files' window, or adds one that runs lazygit
                     (else git status) or yazi (else eza, tree, ls).

With modules.workspace.ahub_keys: true, prefix+A opens a menu with these.

It never starts the hub or a peer: an AHUB slot starts the hub only when you
press Enter, and 'ahub up' is yours to run. It never relayouts a workspace.

```
dot hub <project> [console|status|ui|git|files] [flags]
```

### Examples

```
  dot hub myproject
  dot hub myproject status
```

### Options

```
  -h, --help   help for hub
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

* [dot](dot.md)	 - User environment & workspace management tool

