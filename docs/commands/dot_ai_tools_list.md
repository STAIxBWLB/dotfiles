## dot ai tools list

List selectable tooling or inspect installed CLIs

```
dot ai tools list [flags]
```

### Examples

```
  dot ai tools list
  dot ai tools list --details
```

### Options

```
      --details[=true|false]        show detected CLIs, portable settings and app casks instead of the catalog
  -h, --help[=true|false]           help for list
      --include-auth[=true|false]   show auth/local-secret paths in the detailed inventory
      --json[=true|false]           print the selectable catalog as JSON
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

* [dot ai tools](dot_ai_tools.md)	 - Inspect or reconcile selected development tooling

