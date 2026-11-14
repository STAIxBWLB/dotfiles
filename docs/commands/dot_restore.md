## dot restore

One-stop interactive restore (profile, apps, AI, secrets)

### Synopsis

Restore everything dot manages from a shared backup root in one run.

The wizard confirms the backup root, picks the source host (any machine
that backed up into the same root — cross-host restore), selects domains
and snapshot versions, optionally runs 'dot apply' after the profile
restore (installs packages including age), then restores secrets, app
settings, and AI settings in a safe order. Existing local files are
preserved in pre-restore backup locations that each step reports.

Order: profile → state reload → apply (optional) → secrets → apps → ai.
A profile restore failure aborts the wizard (later steps depend on the
restored state); any other failure is recorded and the run continues.
--version pins both the profile and AI snapshot; use the individual
commands (dot profile restore / dot ai restore) to mix versions.

```
dot restore [flags]
```

### Options

```
      --apply             Run 'dot apply' after the profile restore
      --from string       Backup root (overrides configured BackupRoot)
  -h, --help              help for restore
      --host string       Source hostname to restore from (default: this host)
      --include-age-key   Restore ~/.ssh/age_key* from the profile snapshot
      --include-auth      Restore AI auth tokens from the AI snapshot
      --scope string      Comma-separated domains to restore (profile,apps,ai,secrets)
      --version string    Snapshot version for selected profile/AI domains (default: latest; must exist in each selected domain)
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

