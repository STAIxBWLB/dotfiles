## dot sync owner

Show or set which machine may push this profile

### Synopsis

Show or set which machine may push this profile.

Renaming a Mac: run dot sync owner --rename <old> <new> on that Mac. It
rewrites the owner in every profile of this workspace owned by <old> (mirror
and peer alike), then does the same on the peer over ssh unless --local-only.
Only a profile whose current owner is <old> is renamed. On this Mac the old
name stays as an alias, so the guard and the peer's owner check keep
matching while it still answers to it; a generic name such as "Mac" is not
kept, and the other Mac records no alias (the fence reads the
coordinator's). The coordinator retires its aliases at the first complete
peer sync that finds the peer recording the new owner, once this Mac answers
to the new name. At equal epochs the peer fence refuses a peer that passes
its own owner guard. When the peer cannot be reached, the rename runs only
on a Mac that still answers to <old> (or with --local-only). The epoch,
targets and baselines are untouched, so no run plans a deletion. It refuses
when this Mac answers to neither name, when the peer (or the peer target's
host) answers to either one, and, once this Mac no longer answers to <old>,
unless it runs the peer scheduler and the peer, asked then, runs none
(without a peer, or a peer that cannot answer, only --local-only renames
it): moving ownership between the Macs is --set or dot peer handover.
--local-only skips these checks; it is the step the command runs on the
peer. An unreachable peer exits 0 with that step printed; a peer that fails
the step exits 1. A retry after a peer failure goes on to the peer (unless
<old> was a generic name, which is not kept: then run the printed
--local-only command there).
--dry-run shows the change, and the warnings about names it cannot check,
without writing anything: run it first.

With --store=peer, a --set or --clear that leaves this Mac without the
coordinator role also removes its peer scheduler and runs its on_deactivate
hooks, as a demotion does. A --clear on the Mac that is not the coordinator
leaves it with no owner at an epoch at least the coordinator's (the usual
case), so the coordinator's next peer sync and dot peer setup refuse, without
demoting it, until this Mac records an owner again: dot peer adopt --owner
<coordinator> --epoch <its epoch> (dot sync owner --store=peer --set
<coordinator> on a dot that predates epochs), as the refusal says.

Keep the peer target's ssh alias through a rename: the target is part of the
baseline identity (baseline.peer-target), and editing target: in the peer
config resets the baseline. Point the old alias at the new host name in
~/.ssh/config instead.

```
dot sync owner [--rename <old> <new>] [flags]
```

### Options

```
      --clear        remove the ownership restriction
  -h, --help         help for owner
      --local-only   with --rename, leave the peer alone
      --rename       record a machine rename <old> <new> in every profile, here and on the peer
      --set string   set ownership to a specific machine name
      --set-self     claim ownership for this machine
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync

