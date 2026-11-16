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
      --adopt            repoint an existing ~/.local/bin/<bin> symlink, leaving the other install in place
      --all              install every family tool
  -h, --help             help for install
      --version string   exact X.Y.Z version to install, with one id (default: the registry's latest)
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

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side

