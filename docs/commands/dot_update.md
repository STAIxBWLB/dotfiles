## dot update

Update dot binary to latest version

### Synopsis

Download and install the latest dot release from GitHub.

A Homebrew install is upgraded through the brew that owns it instead: when the
installed tap recipe matches the verified native release, dot uses its bounded
prebuilt update policy. Stale or changed tap metadata uses the full maintenance
gate before brew update. A pinned formula is left alone. Use --homebrew to
target an existing Homebrew install from a standalone dot binary. --check only
reports the latest version and which path an update would take, and why the
verified-prebuilt path is skipped when it is. --wait 30m waits for a busy
maintenance slot instead of deferring at once.

```
dot update [flags]
```

### Options

```
      --check           Only check for updates without installing
  -h, --help            help for update
      --homebrew        Target the installed Homebrew dot formula
      --wait duration   wait up to this long for the maintenance slot instead of deferring (e.g. 30m)
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

