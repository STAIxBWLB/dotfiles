## dot config export

Export configuration to a portable YAML file

### Synopsis

Export the current dot configuration to stdout or a file.
The exported file can be used on another machine with 'dot init --from <file>'.

```
dot config export [path] [flags]
```

### Examples

```
  dot config export
  dot config export ~/dotfiles-config.yaml
```

### Options

```
  -h, --help[=true|false]   help for export
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

* [dot config](dot_config.md)	 - Show current configuration

