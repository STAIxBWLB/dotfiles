## dot completion fish

Generate the fish completion script

### Synopsis

Generate the autocompletion script for fish.

Load it in the current shell session with the first example. To load it in
every new session, write the script to ~/.config/fish/completions/dot.fish,
then start a new shell.

```
dot completion fish [flags]
```

### Examples

```
  dot completion fish | source
```

### Options

```
  -h, --help              help for fish
      --no-descriptions   disable completion descriptions
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

* [dot completion](dot_completion.md)	 - Generate a shell completion script

