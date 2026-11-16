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

### Examples

```
  dot restore
  dot restore --scope profile --apply
```

### Options

```
      --apply             run 'dot apply' after the profile restore
      --from string       backup root (overrides configured BackupRoot)
  -h, --help              help for restore
      --host string       source hostname to restore from (default: this host)
      --include-age-key   restore ~/.ssh/age_key* from the profile snapshot
      --include-auth      restore AI auth tokens from the AI snapshot
      --scope string      comma-separated domains to restore (profile,apps,ai,secrets)
      --version string    snapshot version for selected profile/AI domains (default: latest; must exist in each selected domain)
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

