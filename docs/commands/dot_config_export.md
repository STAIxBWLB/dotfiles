## dot config export

Export configuration to a portable YAML file

### Synopsis

Export the current dot configuration to stdout or a file.
The exported file can be used on another machine with 'dot init --from <file>'.

```
dot config export [path] [flags]
```

### Options

```
  -h, --help   help for export
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot config](dot_config.md)	 - Show current configuration

