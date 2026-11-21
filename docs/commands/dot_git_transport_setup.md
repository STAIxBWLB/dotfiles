## dot git transport setup

Run dot git transport periodically as a LaunchAgent

```
dot git transport setup [flags]
```

### Examples

```
  dot git transport setup
  dot git transport setup --interval 5m
```

### Options

```
  -h, --help                help for setup
      --interval duration   how often to probe (default 10m0s)
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

* [dot git transport](dot_git_transport.md)	 - Use HTTPS for GitHub while SSH to GitHub is blocked

