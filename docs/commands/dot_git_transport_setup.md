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
  -h, --help[=true|false]   help for setup
      --interval duration   how often to probe (default 10m0s)
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

* [dot git transport](dot_git_transport.md)	 - Use HTTPS for GitHub while SSH to GitHub is blocked

