## dot sync names trim

Plan or apply trailing-whitespace filename trims

### Synopsis

Trim trailing spaces and tabs from selected workspace file and folder
names. Dropbox and Windows cannot store a name with a trailing space or
period — Dropbox renames it to "<name> (Unicode Encoding Conflict)" on upload
— so push excludes such names and this command renames the whitespace ones.
Names ending in a period are reported by push but left for manual review.

The walk honors the profile's sync filters and hard safety paths, never
follows or renames symlinks, and refuses to run when two siblings would trim
to the same name. Renames apply deepest-first and roll back on error.

```
dot sync names trim [flags]
```

### Options

```
  -h, --help   help for trim
```

### Options inherited from parent commands

```
      --config string        Path to custom config YAML
      --dry-run              Show what would be done without executing
      --filter-mode string   override config filter mode for this run: include or exclude
      --home string          Override home directory (for admin setup of other users)
      --store string         sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose              Show rsync progress output
      --yes                  Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync names](dot_sync_names.md)	 - Maintain workspace filenames (Unicode NFD, trailing whitespace)

