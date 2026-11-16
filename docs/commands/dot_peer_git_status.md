## dot peer git status

Classify every workspace repo against its recorded commit

```
dot peer git status [<repo>...] [flags]
```

### Examples

```
  dot peer git status
  dot peer git status --json
```

### Options

```
  -h, --help   help for status
      --json   print a stable machine-readable status document
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

* [dot peer git](dot_peer_git.md)	 - Realign HEAD and index with the files peer sync delivered

