## dot remote src

Clone or analyze third-party source on the host, never on this machine

### Synopsis

Run COMMAND in a container in the host's third-party source area: src_root
(default ~/dot-remote-src) without NAME, which is where a clone is made, or
src_root/NAME with it. Untrusted code is cloned and analyzed only on the host:
nothing local is sent, nothing comes back but output, and no cache volume is
mounted, so it cannot reach the caches trusted builds reuse.

  dot remote src -- git clone https://example.com/x.git x
  dot remote src x -- rg -n unsafe

A NAME the host has no clone of exits 66 (EX_NOINPUT).

```
dot remote src [NAME] -- COMMAND [ARG...] [flags]
```

### Options

```
  -h, --help   help for src
      --json   print the defer record as JSON
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --host string     remote host to use (default: the matching project's host, then remote.default)
      --verbose         Show library and debug logs on stderr
      --wait duration   retry a held lease for this long before deferring (default 0: defer at once)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host

