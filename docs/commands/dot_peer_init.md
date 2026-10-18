## dot peer init

Create the peer profile pointing at another machine

### Synopsis

Write <workspace>/.dotfiles/peer/ with a target, secrets opt-in, and the
host-path list.

Use a host name that survives a network change: an SSH config alias that
maps to the other machine's private-network address (for example its
Cloudflare WARP IPv6, with HostKeyAlias), so the profile target does not
change when the address does. A laptop needs one that works without an inbound port.

```
dot peer init [flags]
```

### Options

```
  -h, --help                 help for init
      --host string          ssh destination for the other machine (user@host)
      --remote-path string   workspace path on the peer (default: same as local)
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH

