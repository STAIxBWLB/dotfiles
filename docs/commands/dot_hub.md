## dot hub

Open a registered project's agent-hub console, status or dashboard

### Synopsis

Open agent-hub (ahub) for a registered project, at its registered root.

  console (default)  Outside tmux, runs 'ahub --project <root> console' in this
                     terminal. Inside that project's workspace session, focuses
                     its AHUB pane, or adds one 'hub' window to a session
                     without one. Elsewhere in tmux it prints how to switch.
  status             Runs 'ahub --project <root> status' once.
  ui                 Runs 'ahub --project <root> ui' (the local dashboard).

It never starts the hub or a peer: an AHUB slot starts the hub only when you
press Enter, and 'ahub up' is yours to run. It never relayouts a workspace.

```
dot hub <project> [console|status|ui] [flags]
```

### Options

```
  -h, --help   help for hub
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

* [dot](dot.md)	 - User environment & workspace management tool

