## dot ws init

Clone configured workspace repos recursively

### Synopsis

Clone each configured workspace repo (e.g. work, vault) into <workspace.path>/<name>
with --recurse-submodules. The repo named "vault" clones into the configured
vault location instead (workspace.vault; detected as <workspace.path>/work/vault
or <workspace.path>/vault when unset).

Targets that are missing, empty, or contain only a .gdrive symlink are cloned
without --force (the .gdrive symlink is preserved). Populated targets are
skipped unless --force is given, in which case contents are deleted and the
repo is re-cloned.

```
dot ws init [flags]
```

### Options

```
      --force   Re-clone over populated targets (destructive)
  -h, --help    help for init
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ws](dot_ws.md)	 - Dual-workspace (work + cloud mirror) folder operations

