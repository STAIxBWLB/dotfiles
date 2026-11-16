## dot completion zsh

Generate the zsh completion script

### Synopsis

Generate the autocompletion script for zsh. If completion is not enabled in
your environment yet, add 'autoload -U compinit; compinit' to ~/.zshrc first.

Load it in the current shell session with the first example. To load it in
every new session, write the script to "${fpath[1]}/_dot" on Linux or to
$(brew --prefix)/share/zsh/site-functions/_dot on macOS, then start a new
shell.

```
dot completion zsh [flags]
```

### Examples

```
  source <(dot completion zsh)
```

### Options

```
  -h, --help              help for zsh
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

