## dot peer home-paths

Read or replace the peer host-path allowlist

```
dot peer home-paths [flags]
```

### Options

```
  -h, --help   help for home-paths
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

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH
* [dot peer home-paths get](dot_peer_home-paths_get.md)	 - Read the peer host-path list
* [dot peer home-paths set](dot_peer_home-paths_set.md)	 - Replace the peer host-path list from stdin
* [dot peer home-paths tracked](dot_peer_home-paths_tracked.md)	 - Read or replace the tracked host-path list (deletes propagate, conflicts quarantine)

