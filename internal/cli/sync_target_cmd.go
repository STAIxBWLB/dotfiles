package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

// dot sync target, mirror, init and owner: the per-workspace store's
// destination, layout and writer.

func newSyncTargetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "target [spec]",
		Short: "Show or set the sync target (local mirror dir or SSH remote)",
		Long: `With no argument, prints the resolved sync target.

With a spec, sets the target in this workspace's local config
(<workspace>/.dotfiles/sync/config.yaml) so it takes effect immediately.
Accepted forms:

  local:~/Dropbox/work       local directory (a cloud client's folder)
  ssh:user@host:~/work       rsync over SSH
  ~/Dropbox/work             bare path — shorthand for local:

Local targets are also recorded in the global user state so future
workspaces inherit them.`,
		Args:         cobra.MaximumNArgs(1),
		RunE:         runSyncTarget,
		SilenceUsage: true,
	}
}

func newSyncMirrorCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "mirror [path]",
		Short:        "Show or set the local mirror path",
		Deprecated:   "use 'dot sync target' instead.",
		Args:         cobra.MaximumNArgs(1),
		RunE:         runSyncTarget,
		SilenceUsage: true,
	}
}

func runSyncTarget(cmd *cobra.Command, args []string) error {
	p := printerFrom(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	home := homeFromCmd(cmd) // honors the persistent --home override

	var target syncer.Target
	if len(args) == 1 {
		t, err := syncer.ParseTarget(args[0])
		if err != nil {
			return err
		}
		if t.Kind == syncer.TargetLocal {
			t.Path = appsettings.ExpandHome(t.Path, home)
		}
		target = t
	}

	// Print + dry-run are read-only: use the read-only bootstrap so neither
	// creates the per-workspace .dotfiles/sync layout or touches
	// .gitignore on first use.
	if len(args) == 0 || dryRun {
		bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
		if err != nil {
			return err
		}
		if len(args) == 0 {
			p.KV("Target", bs.Config.Target.String())
			return nil
		}
		p.Line("[dry-run] would set target to %s (local config%s)", target.String(),
			map[bool]string{true: " + global state", false: ""}[target.Kind == syncer.TargetLocal])
		return nil
	}

	homeOverride, _ := cmd.Flags().GetString("home")

	// Local config governs the current workspace (global state is ignored
	// once it exists), so write it for immediate effect — but only for the
	// current user. Under --home the admin isn't in the target user's
	// workspace, so the local config (always current-workspace) doesn't
	// apply; only the home-aware global state below is meaningful there.
	if homeOverride == "" {
		bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
		if err != nil {
			return err
		}
		if err := syncer.SetLocalTarget(bs.Config, target); err != nil {
			return err
		}
	}

	// Global state, home-aware: local targets are inherited by future
	// workspaces. SSH targets stay workspace-local.
	if target.Kind == syncer.TargetLocal {
		state, err := loadStateForCmd(cmd)
		if err != nil {
			return fmt.Errorf("load global state: %w", err)
		}
		state.Modules.Gsync.MirrorPath = target.Path
		if err := persistUserState(cmd, state); err != nil {
			p.Warn("could not update global state: %v", err)
		}
	}

	p.Line("%s", ui.StyleSuccess.Render("✓ sync target set"))
	p.KV("Target", target.String())
	return nil
}

func newSyncInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize <workspace>/.dotfiles/sync/ from current state",
		Long: `One-time onboarding for the per-workspace store. Creates
<workspace>/.dotfiles/sync/ with config.yaml, include.txt, exclude.txt,
ignore.txt, manifests, log dir; appends '/.dotfiles/' to <workspace>/.gitignore
so the store is never committed; and creates <workspace>/inbox/gdrive/ if
missing.

Idempotent — re-running on a populated store leaves operator edits intact and
just heals any missing pieces.`,
		RunE:         runSyncInit,
		SilenceUsage: true,
	}
}

func runSyncInit(cmd *cobra.Command, _ []string) error {
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	p := printerFrom(cmd)
	res, err := syncer.InitStore(bs.Config, dryRun)
	if err != nil {
		return err
	}
	if res.DryRun {
		if res.LegacyStoreDir != "" {
			p.Line("dry-run: would rename %s to %s", res.LegacyStoreDir, res.StoreDir)
		}
		p.Line("dry-run: would create %s", res.StoreDir)
		for _, f := range []string{res.ConfigFile, res.IncludeFile, res.IgnoreFile} {
			p.Line("dry-run: would create %s", f)
		}
		p.Line("dry-run: would create %s", res.InboxDir)
		p.Line("dry-run: would append the managed block to %s", res.WorkspaceIgnore)
		return nil
	}

	p.Header("gsync workspace initialized")
	p.KV("Store", res.StoreDir)
	p.KV("Workspace", res.Workspace)
	p.KV("Mirror", res.Mirror)
	p.KV("Propagation", res.Propagation.String())
	p.KV("Filter mode", res.FilterMode.String())
	p.KV("Inbox staging", res.InboxDir)
	p.Blank()
	p.Line("Edit %s to customize behavior; %s for include patterns; %s for additional ignore patterns.", res.ConfigFile, res.IncludeFile, res.IgnoreFile)
	p.Line("Run 'dot sync setup' to verify rsync and keep automatic sync disabled unless intervals are passed.")
	return nil
}

// newSyncOwnerCmd shows or moves the writer of a profile.
//
// The mirror is a single-writer channel: each machine keeps its own baseline and
// the mirror profile runs with delete propagation on, so two pushers take turns
// undoing each other. Ownership is therefore an explicit, recorded decision
// rather than something inferred at run time.
func newSyncOwnerCmd() *cobra.Command {
	var setSelf bool
	var setTo string
	var clearOwner bool
	var rename, localOnly bool
	cmd := &cobra.Command{
		Use:   "owner [--rename <old> <new>]",
		Short: "Show or set which machine may push this profile",
		Long: `Show or set which machine may push this profile.

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

With --profile=peer, a --set or --clear that leaves this Mac without the
coordinator role also removes its peer scheduler and runs its on_deactivate
hooks, as a demotion does. A --clear on the Mac that is not the coordinator
leaves it with no owner at an epoch at least the coordinator's (the usual
case), so the coordinator's next peer sync and dot peer setup refuse, without
demoting it, until this Mac records an owner again: dot peer adopt --owner
<coordinator> --epoch <its epoch> (dot sync owner --profile=peer --set
<coordinator> on a dot that predates epochs), as the refusal says.

Keep the peer target's ssh alias through a rename: the target is part of the
baseline identity (baseline.peer-target), and editing target: in the peer
config resets the baseline. Point the old alias at the new host name in
~/.ssh/config instead.`,
		Args: func(c *cobra.Command, args []string) error {
			if rename {
				return cobra.ExactArgs(2)(c, args)
			}
			return cobra.NoArgs(c, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			if rename {
				return runSyncOwnerRename(c, args[0], args[1], localOnly)
			}
			if localOnly {
				return fmt.Errorf("--local-only only applies to --rename")
			}
			return runSyncOwner(c, syncer.OwnerOptions{Clear: clearOwner, SetSelf: setSelf, SetTo: setTo})
		},
	}
	cmd.Flags().BoolVar(&setSelf, "set-self", false, "claim ownership for this machine")
	cmd.Flags().StringVar(&setTo, "set", "", "set ownership to a specific machine name")
	cmd.Flags().BoolVar(&clearOwner, "clear", false, "remove the ownership restriction")
	cmd.Flags().BoolVar(&rename, "rename", false, "record a machine rename <old> <new> in every profile, here and on the peer")
	cmd.Flags().BoolVar(&localOnly, "local-only", false, "with --rename, leave the peer alone")
	cmd.MarkFlagsMutuallyExclusive("rename", "set", "set-self", "clear")
	return cmd
}

// runSyncOwnerRename migrates every profile owned by oldName, then the peer's
// profiles over ssh. It runs on the machine being renamed, and never moves
// ownership to or from the peer: that is --set or a handover, with an epoch.
// A peer that cannot be reached or updated is reported with the command to
// run there; the alias keeps the pair working meanwhile.
func runSyncOwnerRename(cmd *cobra.Command, oldName, newName string, localOnly bool) error {
	p := printerFrom(cmd)
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	// Read-only: the rename needs only the workspace root, and a refused
	// rename must not migrate any store on the way.
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, true))
	if err != nil {
		return err
	}
	names := syncer.MachineNames()
	manual := "dot sync owner --rename " + shellArg(oldName) + " " + shellArg(newName) + " --local-only"

	var peer *syncer.BootstrapResult
	if !localOnly {
		answersOld := answersTo(names, oldName)
		if !answersOld && !answersTo(names, newName) {
			return fmt.Errorf("this machine answers to %s, neither %q nor %q; run --rename on the machine being renamed", strings.Join(names, ", "), oldName, newName)
		}
		var perr error
		peer, perr = peerBootstrapReadOnly(cmd)
		switch {
		case perr != nil:
			p.Warn("peer profile could not be read (%v); the peer will not be migrated", perr)
			peer = nil
		case !peer.Config.Target.IsSSH():
			peer = nil
		}
		checked := false
		var peerView *syncer.PeerView
		if peer != nil {
			// The target host names the other Mac. A user@ prefix is not part
			// of the host.
			host := peer.Config.Target.Host
			if i := strings.LastIndex(host, "@"); i >= 0 {
				host = host[i+1:]
			}
			if answersTo([]string{host}, oldName) || answersTo([]string{host}, newName) {
				return fmt.Errorf("the peer target %q is %q or %q: a rename cannot move ownership between the Macs; use dot sync owner --set or dot peer handover", peer.Config.Target.Host, oldName, newName)
			}
			view, err := syncer.PeerOwnerView(cmd.Context(), probeRunner(), peer.Config)
			switch {
			case err != nil:
				p.Warn("peer %s could not be read (%v); it cannot be checked or migrated now", peer.Config.Target.Host, err)
				peer = nil
			case answersTo(view.MachineNames, oldName) || answersTo(view.MachineNames, newName):
				return fmt.Errorf("the peer answers to %s, which includes %q or %q: a rename cannot move ownership between the Macs; use dot sync owner --set or dot peer handover", strings.Join(view.MachineNames, ", "), oldName, newName)
			case len(view.MachineNames) == 0:
				// No names is no answer: it cannot show it is neither name.
				p.Warn("peer %s reported no machine names; it cannot be checked now", peer.Config.Target.Host)
				peer = nil
			default:
				checked, peerView = true, view
			}
		}
		// Once this Mac no longer answers to <old>, nothing in the names
		// shows that it, and not the other Mac, is the owner being renamed:
		// after both host renames neither answers to <old>. Both sides must
		// then agree which Mac runs the owner's peer scheduler: this one does
		// and the peer, asked just now, does not. Anything less (no peer
		// profile, a peer store that does not load, a peer that cannot
		// answer, a stale plist on both) refuses; --local-only on the Mac
		// being renamed stays the explicit override.
		if !answersOld {
			root := strings.TrimRight(bs.Config.LocalPath, "/")
			switch {
			case !peerStoreExists(root):
				return fmt.Errorf("this machine no longer answers to %q and there is no peer to confirm which Mac is the owner; run --rename while the owner still answers to %q, or with --local-only on the owner", oldName, oldName)
			case !checked:
				return fmt.Errorf("the peer could not confirm that it is not %q or %q, and this machine no longer answers to %q; wake the peer and retry, or run with --local-only on the Mac being renamed", oldName, newName, oldName)
			case runtime.GOOS != "darwin":
				return fmt.Errorf("this machine no longer answers to %q, and the scheduler proof of ownership needs launchd (macOS); run with --local-only on the owner of each profile", oldName)
			case !runsPeerScheduler(bs):
				return fmt.Errorf("this machine no longer answers to %q and does not run the peer scheduler, so it cannot be shown to be the owner being renamed; run --rename on the coordinator (the Mac with the peer scheduler), or, when the mirror and the peer have different owners, --local-only on the owner of each profile", oldName)
			case peerView.Scheduler == "" || strings.HasPrefix(peerView.Scheduler, "unknown"):
				return fmt.Errorf("the peer could not report its peer scheduler (%q), so neither Mac can be shown to be the owner being renamed; check it there with dot peer status, or run with --local-only on the owner", peerView.Scheduler)
			case peerView.Scheduler != syncer.SchedulerNotInstalled.String() && !strings.HasPrefix(peerView.Scheduler, "unsupported"):
				// A peer without launchd (unsupported) runs no scheduler.
				return fmt.Errorf("both Macs claim the owner's peer scheduler (the peer reports %q), so neither can be shown to be the owner being renamed; remove the stale one with dot peer setup --off there, or run with --local-only on the owner", peerView.Scheduler)
			}
		}
	}

	root := strings.TrimRight(bs.Config.LocalPath, "/")
	// Said before anything is written: --dry-run shows them with no write.
	switch {
	case localOnly:
	case peer == nil && peerStoreExists(root):
		p.Warn("the peer was not checked: make sure %q is not the other Mac's name", newName)
	case peer != nil && !answersTo(names, newName):
		// Before this Mac's host rename, <new> is checked only against the
		// other Mac's current names, not the one it may get next.
		p.Warn("%q was checked only against the other Mac's current names: make sure it is not the name the other Mac will get", newName)
	}
	// Only a Mac that answers to one of the names keeps <old> as an alias;
	// the other Mac's step (--local-only there) needs none.
	keepAlias := answersTo(names, oldName) || answersTo(names, newName)
	res, err := syncer.RenameOwner(root, oldName, newName, dryRun, keepAlias)
	if localOnly && errors.Is(err, syncer.ErrNoProfileOwned) {
		// The peer step on a Mac that owns nothing by that name: done.
		p.Line("no profile here is owned by %q; %s", oldName, nothingToRename)
		return nil
	}
	if err != nil {
		return err
	}
	verb := "owner"
	if dryRun {
		verb = "dry-run: would set owner"
	}
	kept := "kept as an alias"
	switch {
	case !keepAlias:
		kept = "not kept: this Mac is neither name"
	case !res.OldKept:
		kept = "a generic name, not kept"
	}
	for _, profile := range res.Profiles {
		p.Success("profile %q: %s %q (was %q, %s)", profile, verb, newName, oldName, kept)
	}
	for _, profile := range res.Already {
		p.Success("profile %q: already renamed to %q", profile, newName)
	}
	p.KV("this machine", strings.Join(names, ", "))
	if localOnly {
		return nil
	}
	if peer == nil {
		// An unreachable peer is the expected case (a laptop asleep): the
		// alias keeps the pair working, so it exits 0 with the step to run.
		if peerStoreExists(root) {
			p.Line("  On the other Mac, when reachable: %s", manual)
		}
		return nil
	}
	host := peer.Config.Target.Host
	if dryRun {
		p.Line("dry-run: would run on %s: %s", host, manual)
		return nil
	}
	out, err := syncer.RenamePeerOwner(cmd.Context(), probeRunner(), peer.Config, oldName, newName)
	if err != nil {
		p.Warn("peer %s not migrated: %v", host, err)
		p.Line("  Run there: %s", manual)
		return fmt.Errorf("renamed here, but the peer %s was not migrated", host)
	}
	if strings.Contains(out, nothingToRename) {
		p.Line("peer %s: no profile there is owned by %q; nothing to rename", host, oldName)
		return nil
	}
	p.Success("peer %s: profiles owned by %q renamed to %q", host, oldName, newName)
	return nil
}

// nothingToRename is what --local-only prints when no store is owned by
// <old>; the caller on the other Mac reads it from the output.
const nothingToRename = "nothing to rename"

// peerStoreExists reports a peer profile store, loadable or not: a store
// that fails to load must not read as "no peer".
func peerStoreExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".dotfiles", syncer.PeerProfile, "config.yaml"))
	return err == nil
}

// runsPeerScheduler reports the peer scheduler's plist on this machine.
// Alone it proves nothing (a stale plist: by hand, from an older dot, or on
// the old coordinator after a --set run on the other Mac, until its next
// run demotes it); the rename also asks the peer.
func runsPeerScheduler(bs *syncer.BootstrapResult) bool {
	_, err := os.Stat(filepath.Join(bs.Config.HomeDir(), "Library", "LaunchAgents", "com.dotfiles.peer.plist"))
	return err == nil
}

func answersTo(names []string, name string) bool {
	want := syncer.NormalizeHostname(name)
	for _, n := range names {
		if syncer.NormalizeHostname(n) == want {
			return true
		}
	}
	return false
}

// shellArg quotes an argument for a printed command line when it needs it.
func shellArg(s string) string {
	unsafe := func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.'
	}
	if s != "" && strings.IndexFunc(s, unsafe) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func runSyncOwner(cmd *cobra.Command, opts syncer.OwnerOptions) error {
	p := printerFrom(cmd)
	bs, err := syncer.Bootstrap(syncBootstrapOptions(cmd, false))
	if err != nil {
		return err
	}
	cfg := bs.Config
	if cfg.LocalPaths == nil {
		return fmt.Errorf("profile store unresolved")
	}
	names := syncer.MachineNames()

	if !opts.SetSelf && opts.SetTo == "" && !opts.Clear {
		p.KV("profile", cfg.Profile)
		if strings.TrimSpace(cfg.Owner) == "" {
			p.KV("owner", "(unset - any machine may push)")
		} else {
			p.KV("owner", cfg.Owner)
		}
		if len(cfg.OwnerAliases) > 0 {
			p.KV("aliases", strings.Join(cfg.OwnerAliases, ", "))
		}
		p.KV("this machine", strings.Join(names, ", "))
		if err := syncer.CheckOwner(cfg); err != nil {
			p.Warn("this machine may NOT push this profile")
		} else {
			p.Success("this machine may push this profile")
		}
		return nil
	}

	opts.Config = cfg
	opts.DryRun, _ = cmd.Flags().GetBool("dry-run")
	owner, err := syncer.SetOwner(opts)
	if err != nil {
		return err
	}
	switch {
	case opts.DryRun:
		if owner == "" {
			p.Line("dry-run: would clear the owner of profile %q", cfg.Profile)
		} else {
			p.Line("dry-run: would set the owner of profile %q to %q", cfg.Profile, owner)
		}
	case owner == "":
		p.Success("owner cleared for profile %q (any machine may push)", cfg.Profile)
	default:
		p.Success("owner of profile %q is now %q", cfg.Profile, owner)
	}
	// Taking the coordinator role from this Mac takes its peer scheduler
	// too, as a demotion does: a plist left behind would keep failing its
	// runs and read as the owner's in a later --rename (#185).
	if cfg.Profile == syncer.PeerProfile && runsPeerScheduler(bs) {
		after := *cfg
		after.Owner, after.OwnerAliases = owner, nil
		if strings.TrimSpace(owner) == "" || syncer.CheckOwner(&after) != nil {
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			// Like setup --off it runs on_deactivate; the outcomes print.
			res, err := syncer.PeerSchedule(cmd.Context(), syncer.PeerScheduleOptions{Config: cfg, Runner: bs.Runner, Probe: probeRunner(), Off: true, DryRun: dryRun})
			if err != nil {
				if res != nil {
					printPeerHooks(p, res.Hooks)
				}
				verb := "owner set"
				if dryRun {
					verb = "dry-run: owner not set"
				}
				// Under --home the plist is gone and only launchd is left.
				var target *syncer.SchedulerTargetUserActionRequiredError
				if errors.As(err, &target) {
					return fmt.Errorf("%s; this Mac's peer scheduler plist is handled, but: %w", verb, err)
				}
				return fmt.Errorf("%s, but removing this Mac's peer scheduler failed: %w; run dot peer setup --off", verb, err)
			}
			if dryRun {
				p.Line("dry-run: would remove this Mac's peer scheduler (it no longer coordinates)")
			} else {
				p.Success("peer scheduler removed: this Mac no longer coordinates")
			}
			printPeerHooks(p, res.Hooks)
		}
	}
	return nil
}
