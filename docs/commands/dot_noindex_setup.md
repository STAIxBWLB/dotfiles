## dot noindex setup

Install the periodic noindex sweep as a LaunchAgent

```
dot noindex setup [flags]
```

### Examples

```
  dot noindex setup
```

### Options

```
  -h, --help[=true|false]   help for setup
      --interval duration   how often to sweep (default 6h0m0s)
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

* [dot noindex](dot_noindex.md)	 - Keep Spotlight out of build and cache directories

