## dot remote src

Clone or analyze third-party source on the host

### Synopsis

Run COMMAND in a container in the host's third-party source area: src_root
(default ~/dot-remote-src) without NAME, which is where a clone is made, or
src_root/NAME with it. Untrusted code is cloned and analyzed only on the host:
nothing local is sent, nothing comes back but output, and no cache volume is
mounted, so it cannot reach the caches trusted builds reuse.

A NAME the host has no clone of exits 66 (EX_NOINPUT).

```
dot remote src [<name>] -- <command> [<arg>...] [flags]
```

### Examples

```
  dot remote src -- git clone https://example.com/x.git x
  dot remote src x -- rg -n unsafe
```

### Options

```
  -h, --help[=true|false]   help for src
      --json[=true|false]   print the defer record as JSON
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --host string            remote host to use (default: the matching project's host, then remote.default)
      --verbose[=true|false]   show library and debug logs on stderr
      --wait duration          retry a held lease for this long before deferring (default 0: defer at once)
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host

