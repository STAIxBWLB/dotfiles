## dot ai family install

Install family tools and make each one current

### Synopsis

Install each named family tool, or every one with --all, and make it current.

Tools are installed one after another; a failure on one does not stop the
others, and the command exits non-zero naming each tool that failed.

```
dot ai family install <id>... | --all [flags]
```

### Options

```
      --adopt            Repoint an existing ~/.local/bin/<bin> symlink from another install; that install is left in place
      --all              Install every family tool
  -h, --help             help for install
      --version string   Exact X.Y.Z version to install, with one id (default: the registry's latest)
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side, one directory per version

