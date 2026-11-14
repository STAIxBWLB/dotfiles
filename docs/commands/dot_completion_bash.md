## dot completion bash

Generate the autocompletion script for bash

### Synopsis

Generate the autocompletion script for the bash shell.

This script depends on the 'bash-completion' package.
If it is not installed already, you can install it via your OS's package manager.

To load completions in your current shell session:

	source <(dot completion bash)

To load completions for every new session, execute once:

#### Linux:

	dot completion bash > /etc/bash_completion.d/dot

#### macOS:

	dot completion bash > $(brew --prefix)/etc/bash_completion.d/dot

You will need to start a new shell for this setup to take effect.


```
dot completion bash
```

### Options

```
  -h, --help              help for bash
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

