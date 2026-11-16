## dot secrets

Manage encrypted secrets

### Synopsis

Encrypt, backup, restore, and inspect dot secrets using age.

### Options

```
  -h, --help   help for secrets
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
* [dot secrets backup](dot_secrets_backup.md)	 - Copy only encrypted secrets
* [dot secrets init](dot_secrets_init.md)	 - Encrypt SSH key and shell secrets with age
* [dot secrets list](dot_secrets_list.md)	 - List encrypted secrets files
* [dot secrets restore](dot_secrets_restore.md)	 - Decrypt only secrets
* [dot secrets status](dot_secrets_status.md)	 - Check status of secrets files

