## dot guard

Manage Claude Code safety hooks

### Synopsis

Manage dot-owned Claude Code PreToolUse safety hooks.

careful warns before destructive shell commands (rm -rf, DROP TABLE,
git push --force, ...). freeze denies Edit/Write outside a chosen
directory. Hooks live in ~/.claude/settings.json, tagged with a
"# dot-guard" marker; entries owned by other tools are never touched.

Guard is a guardrail, not a sandbox: careful only inspects the Bash
tool, and freeze cannot stop shell writes (sed, tee, ...). Temp dirs
and ~/.claude/plans stay writable while frozen.

```
dot guard [flags]
```

### Examples

```
  dot guard --help
```

### Options

```
  -h, --help[=true|false]   help for guard
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot guard disable](dot_guard_disable.md)	 - Remove guard hook entries and clear guard state
* [dot guard enable](dot_guard_enable.md)	 - Register guard PreToolUse hooks in ~/.claude/settings.json
* [dot guard freeze](dot_guard_freeze.md)	 - Deny Edit/Write outside the given directory
* [dot guard status](dot_guard_status.md)	 - Show hook registration, state and binary health
* [dot guard unfreeze](dot_guard_unfreeze.md)	 - Clear the freeze boundary (hooks stay registered)

