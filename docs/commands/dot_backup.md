## dot backup

One-stop interactive backup (profile, apps, AI, secrets)

### Synopsis

Back up everything dot manages in one interactive run.

Domains:
  profile  config.yaml + install/backup lists (+ optional age keys)
           → <root>/profiles/<host>/<version>/
  apps     macOS app settings (plists, Application Support, containers)
           → <root>/app-settings/<host>/<token>/
  ai       AI CLI/agent settings + Maru settings
           → <root>/ai-config/<host>/<version>/
  secrets  encrypted .age archives from the local secrets store
           → <root>/secrets-age/<host>/

The wizard confirms the backup root, lets you pick domains, asks the
per-domain questions (age keys, AI auth tokens, tag), then runs every
selected domain and prints a ✓/✗ summary. Profile and AI snapshots share
one tag so they can be correlated later. Use --yes with --scope for
unattended runs.

```
dot backup [flags]
```

### Options

```
  -h, --help              help for backup
      --include-age-key   Include ~/.ssh/age_key* in the profile snapshot
      --include-auth      Include AI auth tokens in the AI snapshot
      --scope string      Comma-separated domains to back up (profile,apps,ai,secrets)
      --tag string        Shared label stored in profile/AI snapshot metadata
      --to string         Backup root (overrides configured BackupRoot)
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

