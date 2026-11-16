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
  -h, --help   help for set
      --json   print the updated document
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

* [dot peer home-paths](dot_peer_home-paths.md)	 - Read or replace the peer host-path allowlist

