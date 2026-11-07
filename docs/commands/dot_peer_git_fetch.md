## dot peer git fetch

Fetch every workspace repo's upstream, then show where each repo stands

### Synopsis

Fetch every workspace repo (submodules included, linked worktrees left out)
from its upstream remote: the remote HEAD's branch tracks, else origin.
Peer sync carries the files the other Mac committed but not its refs, so
until this runs realign looks for those commits in stale remote-tracking
refs. Then print the status read after the fetch, with each move realign
would make.

Only remote-tracking refs, FETCH_HEAD and objects are written; HEAD, the
index, branches and the worktree stay as they are, and no hook of the repo
runs. Repos are fetched one at a time; a failed fetch is reported on its
repo and does not stop the others. Under --dry-run nothing is fetched.

```
dot peer git fetch [<repo>...] [flags]
```

### Options

```
  -h, --help   help for fetch
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

* [dot peer git](dot_peer_git.md)	 - Realign HEAD and index with the files peer sync delivered

