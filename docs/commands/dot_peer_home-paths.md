## dot peer home-paths

Read or replace the peer host-path allowlist

```
dot peer home-paths [flags]
```

### Examples

```
  dot peer home-paths get
```

### Options

```
  -h, --help[=true|false]   help for home-paths
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

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH
* [dot peer home-paths get](dot_peer_home-paths_get.md)	 - Read the peer host-path list
* [dot peer home-paths set](dot_peer_home-paths_set.md)	 - Replace the peer host-path list from stdin
* [dot peer home-paths tracked](dot_peer_home-paths_tracked.md)	 - Read or replace the tracked host-path list

