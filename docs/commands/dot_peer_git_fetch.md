## dot peer git fetch

Fetch every repo's upstream, then show where it stands

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

### Examples

```
  dot peer git fetch
  dot peer git fetch work/dev/app
```

### Options

```
  -h, --help   help for fetch
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --verbose         show library and debug logs on stderr
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer git](dot_peer_git.md)	 - Realign HEAD and index with the files peer sync delivered

