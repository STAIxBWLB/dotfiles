## dot peer git

Realign HEAD and index with the files peer sync delivered

### Synopsis

Peer sync moves files, not git state: HEAD, index and refs stay behind on
the machine that did not make the commits. After a switch, the newly active
Mac realigns instead of pulling: each repo's HEAD and index move forward to
the descendant commit its files already match, through git's compare-and-swap
ref update. Uncommitted modifications survive and untracked files never block;
the only worktree file git may write is a missing or stale .gitmodules that
realign --apply restores, and no git command it starts runs a hook of the
repo it works in.

Repos with a lock, an operation in progress, unmerged entries or staged
changes are skipped and reported. The Mac that did not push also has stale
remote-tracking refs, so a repo whose files match a pushed commit can look
at-tip: 'dot peer git fetch' fetches every repo's upstream first, and
'realign --apply --fetch' does the same before it moves anything. Nothing
else fetches.

```
dot peer git [flags]
```

### Options

```
  -h, --help   help for git
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
* [dot peer git fetch](dot_peer_git_fetch.md)	 - Fetch every workspace repo's upstream, then show where each repo stands
* [dot peer git realign](dot_peer_git_realign.md)	 - Move HEAD and index to the descendant commit the files already match
* [dot peer git status](dot_peer_git_status.md)	 - Classify every workspace repo against its recorded commit

