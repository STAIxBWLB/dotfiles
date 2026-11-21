## dot ai family install

Install family tools and make each one current

### Synopsis

Install each named family tool, or every one with --all, and make it current.

Tools are installed one after another; a failure on one does not stop the
others, and the command exits non-zero naming each tool that failed.

```
dot ai family install <id>... | --all [flags]
```

### Examples

```
  dot ai family install amem
  dot ai family install --all
```

### Options

```
      --adopt[=true|false]   repoint an existing ~/.local/bin/<bin> symlink, leaving the other install in place
      --all[=true|false]     install every family tool
  -h, --help[=true|false]    help for install
      --version string       exact X.Y.Z version to install, with one id (default: the registry's latest)
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

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side

