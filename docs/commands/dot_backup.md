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
selected domain and prints a pass/fail summary. Profile and AI snapshots share
one tag so they can be correlated later. Use --yes with --scope for
unattended runs.

```
dot backup [flags]
```

### Examples

```
  dot backup
  dot backup --scope profile,apps --tag before-upgrade
```

### Options

```
  -h, --help[=true|false]              help for backup
      --include-age-key[=true|false]   include ~/.ssh/age_key* in the profile snapshot
      --include-auth[=true|false]      include AI auth tokens in the AI snapshot
      --scope string                   comma-separated domains to back up (profile,apps,ai,secrets)
      --tag string                     shared label stored in profile/AI snapshot metadata
      --to string                      backup root (overrides configured BackupRoot)
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

