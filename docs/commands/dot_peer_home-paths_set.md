## dot peer home-paths set

Replace the peer host-path list from stdin

```
dot peer home-paths set [flags]
```

### Examples

```
  dot peer home-paths set < home-paths.txt
```

### Options

```
  -h, --help[=true|false]   help for set
      --json[=true|false]   print the updated document
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

* [dot peer home-paths](dot_peer_home-paths.md)	 - Read or replace the peer host-path allowlist

