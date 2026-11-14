## dot completion fish

Generate the autocompletion script for fish

### Synopsis

Generate the autocompletion script for the fish shell.

To load completions in your current shell session:

	dot completion fish | source

To load completions for every new session, execute once:

	dot completion fish > ~/.config/fish/completions/dot.fish

You will need to start a new shell for this setup to take effect.


```
dot completion fish [flags]
```

### Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot completion](dot_completion.md)	 - Generate the autocompletion script for the specified shell

