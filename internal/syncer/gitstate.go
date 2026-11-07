package syncer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// GitRepoStatus classifies one discovered repository in a peer git run.
type GitRepoStatus string

const (
	// GitRepoAligned means the worktree already matches HEAD's own content
	// (gitlinks and .gitmodules aside: the children and peer sync's
	// exclusion account for those).
	GitRepoAligned GitRepoStatus = "aligned"
	// GitRepoRealignable means a strict descendant of HEAD matches the
	// worktree at least as well as HEAD does.
	GitRepoRealignable GitRepoStatus = "realignable"
	// GitRepoRealigned means HEAD and the index were moved to the target.
	GitRepoRealigned GitRepoStatus = "realigned"
	// GitRepoNoMatch means no strict descendant candidate improves on HEAD.
	GitRepoNoMatch GitRepoStatus = "no-match"
	// GitRepoSkipped means the repo was left alone with a recorded reason:
	// a lock, an operation in progress, unmerged entries, staged changes, an
	// unborn HEAD or no checkout at the gitlink path.
	GitRepoSkipped GitRepoStatus = "skipped"
	// GitRepoUnresolvable means an object the classification needs is
	// missing (a vanished parent gitlink, an unenumerable upstream chain).
	GitRepoUnresolvable GitRepoStatus = "unresolvable"
	// GitRepoLinkedWorktree means the checkout is a linked worktree (its
	// gitdir carries a commondir file): listed, never realigned.
	GitRepoLinkedWorktree GitRepoStatus = "linked-worktree"
)

// GitRepoReport is the per-repo outcome of a status or realign run.
type GitRepoReport struct {
	Path   string        `json:"path"` // workspace-relative, "." for the root
	Status GitRepoStatus `json:"status"`
	Reason string        `json:"reason,omitempty"`
	Head   string        `json:"head,omitempty"`
	// HeadDiffs counts tracked worktree differences against HEAD.
	HeadDiffs int `json:"headDiffs,omitempty"`
	// Target is the best candidate commit for a realignable repo.
	Target      string `json:"target,omitempty"`
	TargetDiffs int    `json:"targetDiffs,omitempty"`
	// TargetRef names the ref Target came from when only a --candidate-refs
	// pattern offered it (#217).
	TargetRef  string `json:"targetRef,omitempty"`
	Candidates int    `json:"candidates,omitempty"`
	// TieBreak says which rule chose Target when several candidates matched
	// the worktree equally well (#177), and for a move the parent's own
	// content forces, which children it passes; empty when neither applies.
	TieBreak string `json:"tieBreak,omitempty"`
	// PreviousHead is HEAD's commit before an applied move; Undo is the exact
	// command that restores it (for a branch switch, HEAD's branch too; a
	// default branch the switch created or fast-forwarded stays, and so does
	// a .gitmodules the run restored and synced).
	PreviousHead string `json:"previousHead,omitempty"`
	// Class refines a no-match or skipped outcome (#178), or an
	// unresolvable one whose URL moved or whose gitlink commit is missing
	// (#179), and Suggestion is the one-line next step for it.
	Class      string `json:"class,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
	// RescueTarget is the commit on the upstream or default branch the files
	// match, for a diverged or branch-mismatch repo; --rescue moves there.
	RescueTarget string `json:"rescueTarget,omitempty"`
	// Rescue names the branch that keeps HEAD's commits when --rescue moves
	// the repo; RescueRemote is where it is pushed and RescuePushed says it
	// got there.
	Rescue       string `json:"rescue,omitempty"`
	RescueRemote string `json:"rescueRemote,omitempty"` // empty: kept local (--no-push)
	RescuePushed bool   `json:"rescuePushed,omitempty"`
	// Undo is the exact command that reverses an applied realign or rescue.
	Undo string `json:"undo,omitempty"`
	// Gitmodules reports the worktree .gitmodules against the commit the
	// repo sits on (or moves to): "stale" (an older committed version peer
	// sync never replaced), "missing" (absent from the worktree), "restored"
	// (put back from HEAD by --apply) or "modified" (local edits, left
	// alone). URLMoves lists the submodule URLs that version changes, or the
	// origin --fetch re-pointed, "path: old -> new" (#179); URLUndo puts a
	// re-pointed origin back.
	Gitmodules string   `json:"gitmodules,omitempty"`
	URLMoves   []string `json:"urlMoves,omitempty"`
	URLUndo    string   `json:"urlUndo,omitempty"`
	// Fetch is the outcome of the upstream fetch run before any repo is
	// judged (FetchUpstream, #360): "fetched <remote>", "fetch failed: ..."
	// or "not fetched: ...".
	Fetch string `json:"fetch,omitempty"`

	branch       string // HEAD's branch, empty when detached
	rescueBranch string // branch-mismatch: the default branch HEAD moves onto
	remote       string // remote a rescue branch is pushed to
	rescueDiffs  int    // worktree differences against RescueTarget
	rescueTie    string // how RescueTarget won a tie, shown once a rescue moves
	// rescueRefused is why a rescue would be refused (rescueRefusal);
	// rescueLocalOnly when only a pushed one would be.
	rescueRefused   string
	rescueLocalOnly bool
}

// Classes of no-match and skipped outcomes (#178) and of unresolvable ones
// (url-moved, gitlink-missing; #179).
const (
	GitClassAtTip         = "at-tip"
	GitClassAheadUnpushed = "ahead-unpushed"
	GitClassDiverged      = "diverged"
	// GitClassRewrittenUpstream is a diverged repo whose local-only commits
	// all have a rewritten twin upstream (#216).
	GitClassRewrittenUpstream = "rewritten-upstream"
	GitClassBranchMismatch    = "branch-mismatch"
	GitClassStaleRebaseHead   = "stale-rebase-head"
	GitClassURLMoved          = "url-moved"
	GitClassGitlinkMissing    = "gitlink-missing"
)

// GitStateResult is one status or realign run over the workspace tree.
type GitStateResult struct {
	Root string
	// Git is the git binary resolved to an absolute path once per run
	// (launchd and non-interactive shells do not share the user's PATH).
	Git   string
	Repos []*GitRepoReport // parent-first
}

// CountByStatus tallies the reports per status, in a stable order.
func (r *GitStateResult) CountByStatus() map[GitRepoStatus]int {
	counts := map[GitRepoStatus]int{}
	for _, rep := range r.Repos {
		counts[rep.Status]++
	}
	return counts
}

// GitRepoStatuses is the display/iteration order for statuses.
var GitRepoStatuses = []GitRepoStatus{
	GitRepoAligned,
	GitRepoRealignable,
	GitRepoRealigned,
	GitRepoNoMatch,
	GitRepoSkipped,
	GitRepoUnresolvable,
	GitRepoLinkedWorktree,
}

// PeerGitStatus classifies the workspace root and every submodule,
// recursively and parent first. It writes no objects, no index entries and no
// worktree files. repos optionally restricts the report to
// workspace-relative paths ("." is the root).
func PeerGitStatus(ctx context.Context, root string, repos []string) (*GitStateResult, error) {
	return runGitState(ctx, root, repos, RealignOptions{})
}

// RealignOptions controls PeerGitRealign.
type RealignOptions struct {
	// Apply moves HEAD and index; false is a pure preview.
	Apply bool
	// Rescue also moves diverged and branch-mismatch repos to the commit
	// their files match, after keeping HEAD on a rescue branch (#178).
	Rescue bool
	// NoPush keeps rescue branches local instead of pushing them.
	NoPush bool
	// Fetch lets an applied run fetch a child whose parent gitlink object is
	// missing, after pointing it at a moved submodule URL, then retry it; and
	// fetch a child lacking a commit a candidate of its parent records,
	// before those candidates are judged (#201).
	Fetch bool
	// CandidateRefs adds the commits of refs matching these for-each-ref
	// patterns to every repo's candidates, strict descendants of HEAD only,
	// such as the unpushed branches of a Mac that stopped, fetched into a
	// namespace like refs/peer/<mac>/heads/ (#217).
	CandidateRefs []string
	// FetchUpstream fetches every repo's upstream remote (its branch's
	// remote, else origin) before any repo is judged, so candidates come from
	// what the other Mac pushed rather than from the last fetch (#360).
	FetchUpstream bool
	// Now dates rescue branch names; nil means time.Now.
	Now func() time.Time
}

// PeerGitRealign runs the same classification and, with opts.Apply, moves
// each realignable repo's HEAD and index to its best candidate through git's
// lockfile protocol. Without Apply it is a pure preview and changes nothing
// under .git.
func PeerGitRealign(ctx context.Context, root string, repos []string, opts RealignOptions) (*GitStateResult, error) {
	return runGitState(ctx, root, repos, opts)
}

// gitStateRun carries the per-run state: the resolved git binary, the
// options and the optional repo restriction.
type gitStateRun struct {
	root     string // the workspace, for the restriction's relative paths
	git      string
	opts     RealignOptions
	restrict map[string]bool // nil means all repos
	env      []string        // the environment without git's repo-local variables
	// Answers that hold for the whole run, keyed by repo and full commit ids
	// (a ref name can move, so it is never a key): a present commit stays
	// present, and ancestry between two present commits never changes. A
	// missing commit's ancestry is not cached, so a --fetch can still bring
	// it.
	present  map[string]bool
	ancestry map[string]bool
	// Commits a repo lacks, forgotten after a fetch there (forgetFetched).
	absent map[string]bool
	// Children fetched before their parent's candidates were judged, with
	// the fetch's outcome for the tie line and the origin URL fetched
	// (#201).
	fetched     map[string]string
	fetchedFrom map[string]string
	// contentDiffs, keyed the same way: a run never writes the files it
	// compares (gitlinks and .gitmodules are left out), so a child scored
	// for its parent's question is not scored again in its own turn.
	diffs map[string]int
	// childTarget's answers by child and the gitlink asked about, and the
	// gitlinks each commit's tree records.
	targets map[string]childAnswer
	links   map[string][]gitlinkEntry
	// Answers about two commits' trees (sameOwnContent) and a move's
	// rescue score (rescueDiffs, against the HEAD it names).
	same    map[string]bool
	rescued map[string]int
	// The ref each --candidate-refs candidate came from, by repo and commit.
	candidateRefs map[string]string
	// noHooks' flags for each repo's configured hooks, by repo.
	hookNames map[string][]string
	// Outcome of the FetchUpstream pass, by workspace-relative path.
	upstreamFetch map[string]string
}

// gitCleanEnv drops the variables that pin git to one repository (GIT_DIR,
// GIT_WORK_TREE, GIT_INDEX_FILE, ...): run from a git hook, they would aim
// every per-repo command at the hook's repository. Like git entering a
// submodule, it keeps `git -c` settings (GIT_CONFIG_PARAMETERS,
// GIT_CONFIG_COUNT and its keys): they are the caller's config, not a repo.
// The pathspec variables go too: they would turn off the pathspec magic
// the run's commands rely on (":(exclude)", ":(glob)") (#204).
func gitCleanEnv(ctx context.Context, git string) []string {
	drop := map[string]bool{"GIT_LITERAL_PATHSPECS": true, "GIT_GLOB_PATHSPECS": true, "GIT_NOGLOB_PATHSPECS": true, "GIT_ICASE_PATHSPECS": true}
	if out, err := exec.CommandContext(ctx, git, "rev-parse", "--local-env-vars").Output(); err == nil {
		for _, name := range strings.Fields(string(out)) {
			drop[name] = name != "GIT_CONFIG_PARAMETERS" && name != "GIT_CONFIG_COUNT"
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[name] {
			env = append(env, kv)
		}
	}
	return env
}

func runGitState(ctx context.Context, root string, repos []string, opts RealignOptions) (*GitStateResult, error) {
	root = strings.TrimRight(root, "/")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git not found on PATH: %w", err)
	}
	// LookPath may return a relative path when PATH carries a relative
	// entry; every invocation must resolve to the same absolute binary.
	if gitPath, err = filepath.Abs(gitPath); err != nil {
		return nil, fmt.Errorf("resolving git path: %w", err)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	run := &gitStateRun{root: root, git: gitPath, opts: opts, env: gitCleanEnv(ctx, gitPath)}
	if len(repos) > 0 {
		run.restrict = map[string]bool{}
		for _, arg := range repos {
			run.restrict[normalizeRepoArg(arg)] = true
		}
	}
	result := &GitStateResult{Root: root, Git: gitPath}

	// Validate the restriction against a read-only discovery walk before any
	// mutation, so a mistyped repo argument cannot realign half the tree.
	if run.restrict != nil || opts.FetchUpstream {
		known, err := run.discoverPaths(ctx, root)
		if err != nil {
			return nil, err
		}
		for arg := range run.restrict {
			if !known[arg] {
				return nil, fmt.Errorf("no repo at %q under %s", arg, root)
			}
		}
		if opts.FetchUpstream {
			run.fetchUpstreams(ctx, root, slices.Sorted(maps.Keys(known)))
		}
	}

	run.process(ctx, root, ".", "", "", opts.Apply, result)
	return result, nil
}

func normalizeRepoArg(arg string) string {
	arg = strings.TrimRight(strings.TrimSpace(arg), "/")
	if arg == "" || arg == "." {
		return "."
	}
	return filepath.ToSlash(arg)
}

func (r *gitStateRun) included(relPath string) bool {
	if r.restrict == nil {
		return true
	}
	return r.restrict[relPath]
}

// discoveredPaths lists every repo path the run would visit, without
// classifying anything. Used only to reject unknown restriction arguments
// before a realign starts.
func (r *gitStateRun) discoverPaths(ctx context.Context, root string) (map[string]bool, error) {
	known := map[string]bool{}
	var walk func(abs, rel string) error
	walk = func(abs, rel string) error {
		known[rel] = true
		if _, err := r.gitDir(ctx, abs); err != nil {
			return nil // not a checked-out repo: no children to find
		}
		gitlinks, err := r.childGitlinks(ctx, abs, "HEAD")
		if err != nil {
			return nil // unenumerable: the real pass reports the repo itself
		}
		for _, child := range gitlinks {
			childAbs := filepath.Join(abs, filepath.FromSlash(child.path))
			childRel := rel
			if childRel == "." {
				childRel = child.path
			} else {
				childRel += "/" + child.path
			}
			if err := walk(childAbs, childRel); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, "."); err != nil {
		return nil, err
	}
	return known, nil
}

// fetchUpstreams fetches each repo's upstream remote, one repo at a time and
// before any repo is judged (#360). It writes remote-tracking refs,
// FETCH_HEAD and objects only, runs no hook, and a failure is recorded on
// that repo without stopping the others. Linked worktrees share their main
// checkout's objects and refs, so they are not fetched, and neither is a repo
// the run leaves alone.
// ponytail: sequential; fetch in parallel if a large workspace makes it slow.
func (r *gitStateRun) fetchUpstreams(ctx context.Context, root string, paths []string) {
	r.upstreamFetch = map[string]string{}
	for _, rel := range paths {
		if !r.included(rel) {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		gitdir, err := r.gitDir(ctx, abs)
		if err != nil {
			continue // no checkout; the repo's own turn reports it
		}
		// Locked, staged, conflicting or mid-operation repos are left alone,
		// as fetchToJudge leaves them.
		if reason := r.leftAlone(ctx, abs, gitdir); reason == "linked worktree" {
			continue
		} else if reason != "" {
			r.upstreamFetch[rel] = "not fetched: " + reason
			continue
		}
		remote := r.upstreamRemote(ctx, abs)
		if remote == "" {
			r.upstreamFetch[rel] = "not fetched: no upstream or origin remote"
		} else if err := r.fetchRemote(ctx, abs, remote, rel != "."); err != nil {
			r.upstreamFetch[rel] = "fetch failed: " + shortErr(err)
		} else {
			r.upstreamFetch[rel] = "fetched " + remote
		}
	}
}

// upstreamRemote is the remote HEAD's branch tracks, else origin, else "".
func (r *gitStateRun) upstreamRemote(ctx context.Context, abs string) string {
	if branch, err := r.read(ctx, abs, "symbolic-ref", "-q", "--short", "HEAD"); err == nil && branch != "" {
		if remote, _ := r.read(ctx, abs, "config", "--get", "branch."+branch+".remote"); remote != "" && remote != "." && !strings.HasPrefix(remote, "-") {
			return remote
		}
	}
	if _, err := r.read(ctx, abs, "config", "--get", "remote.origin.url"); err == nil {
		return "origin"
	}
	return ""
}

// process classifies (and optionally realigns) one repo, then recurses into
// its submodule gitlinks. Children are enumerated AFTER the parent has been
// processed, so a child's candidate gitlink is read from the parent's
// realigned HEAD.
func (r *gitStateRun) process(ctx context.Context, abs, rel, gitlink, wantURL string, apply bool, result *GitStateResult) {
	rep := &GitRepoReport{Path: rel}
	included := r.included(rel)
	defer func() {
		if included {
			rep.Fetch = r.upstreamFetch[rel]
			result.Repos = append(result.Repos, rep)
		}
	}()

	gitdir, err := r.gitDir(ctx, abs)
	if err != nil {
		rep.Status = GitRepoSkipped
		if rel == "." {
			rep.Reason = "not a git repository"
		} else {
			rep.Reason = "no checkout at the gitlink path"
		}
		return
	}

	// A linked worktree's gitdir carries a commondir file; a submodule's own
	// gitdir does not. Listed, never realigned, never recursed into.
	if _, statErr := os.Stat(filepath.Join(gitdir, "commondir")); statErr == nil {
		rep.Status = GitRepoLinkedWorktree
		rep.Reason = "linked worktree"
		return
	}

	if included {
		r.judge(ctx, abs, gitdir, gitlink, rep)
		if rep.Status == GitRepoUnresolvable && rep.Reason == gitlinkMissing {
			r.missingGitlink(ctx, abs, gitdir, gitlink, wantURL, apply, rep)
		}
		if r.opts.Rescue && rep.Status == GitRepoNoMatch && rep.RescueTarget != "" {
			r.planRescue(ctx, abs, rep)
		}
		if apply && rep.Status == GitRepoRealignable {
			if rep.Rescue != "" {
				r.rescue(ctx, abs, gitdir, rep)
			} else {
				r.realign(ctx, abs, gitdir, rep)
			}
		}
	}

	// A preview reads the children from the commit the parent would move to,
	// so it shows what --apply does: apply reads them from the moved HEAD.
	rev := "HEAD"
	if included && !apply && rep.Status == GitRepoRealignable {
		rev = rep.Target
	}
	if included {
		r.checkGitmodules(ctx, abs, rev, apply, rep)
	}
	// Each child learns the URL rev's .gitmodules gives it, read even when
	// this repo is outside the restriction, so a child whose gitlink commit
	// is missing can tell a moved URL from a missing fetch.
	urls := r.gitmodulesURLs(ctx, abs, "--blob", rev+":.gitmodules")
	gitlinks, err := r.childGitlinks(ctx, abs, rev)
	if err != nil {
		// Children of a repo whose HEAD cannot be read cannot be discovered;
		// say so on the parent rather than failing the whole run, but do not
		// clobber a more precise classification (an unborn HEAD already says
		// why ls-tree failed).
		if included && (rep.Status == "" || rep.Status == GitRepoAligned) {
			rep.Status = GitRepoUnresolvable
			rep.Reason = "cannot enumerate submodule gitlinks"
		}
		return
	}
	for _, child := range gitlinks {
		childRel := child.path
		if rel != "." {
			childRel = rel + "/" + child.path
		}
		r.process(ctx, filepath.Join(abs, filepath.FromSlash(child.path)), childRel, child.sha, urls[child.path], apply, result)
	}
}

// judge classifies a repo, and again when a child was fetched while it
// was judged (#201): answers given before that fetch may not hold after it.
// Each child fetches once per run, so this ends.
func (r *gitStateRun) judge(ctx context.Context, abs, gitdir, gitlink string, rep *GitRepoReport) {
	for {
		fetches := len(r.fetched)
		r.classify(ctx, abs, gitdir, gitlink, rep)
		if len(r.fetched) == fetches {
			return
		}
		*rep = GitRepoReport{Path: rep.Path}
	}
}

// classify fills rep with the repo's status; it writes nothing, except
// the pre-judgment fetch of a child under --apply --fetch (#201).
func (r *gitStateRun) classify(ctx context.Context, abs, gitdir, gitlink string, rep *GitRepoReport) {
	if reason := r.blockReason(ctx, abs, gitdir); reason != "" {
		rep.Status = GitRepoSkipped
		rep.Reason = reason
		if reason == staleRebaseHead {
			rep.Class = GitClassStaleRebaseHead
			rep.Suggestion = "no rebase is in progress; clear it: git -C " + shellWord(abs) + " update-ref -d REBASE_HEAD"
		}
		return
	}

	head, err := r.read(ctx, abs, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		rep.Status = GitRepoSkipped
		rep.Reason = "no HEAD (unborn branch)"
		return
	}
	rep.Head = head

	headDiffs, err := r.contentDiffs(ctx, abs, gitdir, head)
	if err != nil {
		rep.Status = GitRepoUnresolvable
		rep.Reason = "cannot read HEAD tree: " + shortErr(err)
		return
	}
	rep.HeadDiffs = headDiffs
	// HEAD's tree matching does not end the search: a strict descendant with
	// the same content (empty commits, a net-unchanged sequence, gitlink-only
	// commits, or the parent gitlink) would otherwise leave HEAD behind
	// forever. Moving is fast-forward onto a descendant, so no commit is ever
	// dropped. While HEAD matches, a candidate that cannot be read keeps the
	// repo aligned instead of unresolvable.
	aligned := func() { rep.Status = GitRepoAligned }
	candidates, err := r.candidates(ctx, abs, head, gitlink)
	if err != nil {
		// A missing parent gitlink commit is reported even when HEAD's own
		// content matches: its content can be identical while the parent
		// already points past HEAD, and only the report leads to the fetch.
		if headDiffs == 0 && err.Error() != gitlinkMissing {
			aligned()
			return
		}
		rep.Status = GitRepoUnresolvable
		rep.Reason = err.Error()
		return
	}
	rep.Candidates = len(candidates)
	if len(candidates) == 0 {
		if headDiffs == 0 {
			aligned()
			return
		}
		rep.Status = GitRepoNoMatch
		rep.Reason = "no strict descendant candidate"
		r.classifyNoMatch(ctx, abs, gitdir, rep)
		return
	}

	var tied []string
	bestDiffs := -1
	for _, cand := range candidates {
		diffs, err := r.contentDiffs(ctx, abs, gitdir, cand)
		if err != nil {
			if headDiffs == 0 {
				aligned()
				return
			}
			rep.Status = GitRepoUnresolvable
			rep.Reason = "cannot read candidate tree: " + shortErr(err)
			return
		}
		switch {
		case bestDiffs < 0 || diffs < bestDiffs:
			bestDiffs, tied = diffs, []string{cand}
		case diffs == bestDiffs:
			tied = append(tied, cand)
		}
	}
	if bestDiffs > headDiffs {
		if headDiffs == 0 {
			aligned()
			return
		}
		rep.Status = GitRepoNoMatch
		rep.Reason = "no descendant commit improves on HEAD"
		rep.Target = tied[0]
		rep.TargetDiffs = bestDiffs
		r.classifyNoMatch(ctx, abs, gitdir, rep)
		return
	}
	// A tie with HEAD (best == headDiffs) is still a realign unless the
	// children say HEAD is right: moving to a strict descendant never makes
	// the own-content classification worse, and a parent whose only drift is
	// in its children's gitlinks must move so each child reads the new
	// gitlinks. But a clean parent must not move past gitlink bumps whose
	// content its children do not have yet.
	headTie := ""
	if bestDiffs == headDiffs {
		headTie = head
	}
	target, rule, stay := r.breakTie(ctx, abs, gitlink, headTie, headDiffs > 0, tied)
	rep.TieBreak = rule
	if stay {
		if headDiffs == 0 {
			aligned()
			return
		}
		rep.Status = GitRepoNoMatch
		rep.Reason = "HEAD wins the tie with its descendants (see tie)"
		r.classifyNoMatch(ctx, abs, gitdir, rep)
		return
	}
	rep.Status = GitRepoRealignable
	rep.Target = target
	rep.TargetDiffs = bestDiffs
	rep.TargetRef = r.candidateRefs[abs+"\x00"+target]
}

// breakTie picks among candidates whose own content matches the worktree
// equally well. Content excludes gitlinks, so a run of gitlink-only commits
// ties, and taking the nearest one (the old rule) moved a parent to its
// oldest candidate and every child to a stale gitlink (#177). In order:
//
//  1. children: each child tells where its own turn ends with the gitlink a
//     candidate records (childTarget), and a candidate may not record a
//     gitlink the child does not end at or past; of the rest, those whose
//     gitlinks name exactly where the children end win.
//     When HEAD ties too (head is set), it competes, and stay reports that
//     it won or that no candidate was left. On an inexact tie (edited
//     files) a candidate whose own content differs from HEAD's drops out
//     first: the edits could sit on either, and staying moves nothing;
//  2. the parent's gitlink, which keeps the parent's status clean;
//  3. the newest candidate, the upstream tip side of the chain.
//
// It returns the choice and a one-line account of the rule, empty when a
// lone candidate had nothing to beat and a forced move passes no child.
//
// A forced move (no head: the parent's own content needs it, whatever the
// children hold) also names the children it passes, so the user is warned
// before a commit -a records a rewind (#189 rounds 16-20).
func (r *gitStateRun) breakTie(ctx context.Context, abs, gitlink, head string, inexact bool, tied []string) (string, string, bool) {
	choice, rule, stay := r.pickTied(ctx, abs, gitlink, head, inexact, tied)
	if head == "" && !stay {
		if past := r.passedChildren(ctx, abs, choice); past != "" {
			if rule != "" {
				rule += "; "
			}
			rule += shortRev(choice) + " is past what " + past + " holds, so a commit -a before they catch up records them going back (or, for one never delivered, removed)"
		}
	}
	return choice, rule, stay
}

func (r *gitStateRun) pickTied(ctx context.Context, abs, gitlink, head string, inexact bool, tied []string) (string, string, bool) {
	contenders := tied
	prefix := fmt.Sprintf("%d candidates tie on content; ", len(tied))
	if head != "" {
		prefix = fmt.Sprintf("HEAD and %d candidate(s) tie on content; ", len(tied))
		if inexact {
			tied = slices.DeleteFunc(slices.Clone(tied), func(c string) bool { return !r.sameOwnContent(ctx, abs, head, c) })
			if len(tied) == 0 {
				return "", prefix + "the edited files differ from every candidate's own content, so HEAD stays", true
			}
		}
		contenders = append([]string{head}, tied...)
	}
	if len(contenders) == 1 {
		return tied[0], "", false
	}
	pool, children := r.bestByChildren(ctx, abs, contenders, head != "")
	if len(pool) == 0 {
		return "", prefix + children + ", so HEAD stays", true
	}
	if len(pool) == 1 {
		if children == "" {
			children = "no child tells them apart"
		}
		return pool[0], prefix + children, false
	}
	if children != "" {
		prefix += children + ", then "
	}
	for _, cand := range pool {
		if cand == gitlink {
			return cand, prefix + "parent gitlink", false
		}
	}
	return r.newest(ctx, abs, pool), prefix + "newest candidate", false
}

// newest is the pool member that descends from every other one, so the
// choice does not depend on list order (a child's own run and its parent's
// question list its commits differently). Without one, the last in order:
// the first-parent chain runs nearest HEAD first; a --candidate-refs
// commit only when nothing else is left, since those only widen the pool
// (#217). One merge-base call answers it: the only independent commit of
// the pool is that member.
func (r *gitStateRun) newest(ctx context.Context, abs string, pool []string) string {
	out, err := r.read(ctx, abs, append([]string{"merge-base", "--independent"}, pool...)...)
	if err == nil && slices.Contains(pool, out) {
		return out
	}
	for i := len(pool) - 1; i >= 0; i-- {
		if r.candidateRefs[abs+"\x00"+pool[i]] == "" {
			return pool[i]
		}
	}
	return pool[len(pool)-1]
}

// bestByChildren is childrenEvidence for breakTie: the candidates left
// (none when HEAD stays) and an account of the children's evidence, empty
// when it decided nothing and no commit was missing.
func (r *gitStateRun) bestByChildren(ctx context.Context, abs string, tied []string, withHead bool) ([]string, string) {
	ev := r.childrenEvidence(ctx, abs, tied, withHead)
	note := ""
	if len(ev.unknown) > 0 {
		note = " (" + lacking(ev.unknown) + "; fetch it there, from the URL the candidate's .gitmodules names if it moved, then realign again)"
	}
	if len(ev.fetched) > 0 {
		note += " (" + strings.Join(ev.fetched, "; ") + ")"
	}
	pool := ev.pool
	if withHead {
		if len(pool) == 1 && pool[0] == tied[0] {
			why := "the children are where HEAD's gitlinks say"
			if len(ev.behind) > 0 {
				why = "the children have not reached a candidate's gitlinks (" + strings.Join(ev.behind, ", ") + ")"
			}
			return nil, why + note
		}
		pool = slices.DeleteFunc(slices.Clone(pool), func(c string) bool { return c == tied[0] })
	}
	switch {
	case ev.split:
		var why []string
		if ev.decided > 0 {
			why = append(why, fmt.Sprintf("children are at %d of %d differing gitlinks", ev.best, ev.decided))
		}
		if len(ev.behind) > 0 {
			why = append(why, "not reached: "+strings.Join(ev.behind, ", "))
		}
		return pool, strings.Join(why, "; ") + note
	case note != "":
		return pool, "children cannot tell" + note
	}
	return pool, ""
}

// passedChildren lists the children whose gitlink the move to choice
// changes and whose own turn will not end at or past it (or that lack the
// commit, or were never delivered).
func (r *gitStateRun) passedChildren(ctx context.Context, abs, choice string) string {
	head, err := r.read(ctx, abs, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	now, err := r.childGitlinks(ctx, abs, head)
	if err != nil {
		return ""
	}
	next, err := r.childGitlinks(ctx, abs, choice)
	if err != nil {
		return ""
	}
	was := map[string]string{}
	for _, e := range now {
		was[e.path] = e.sha
	}
	var past []string
	for _, e := range next {
		if e.sha == was[e.path] {
			continue
		}
		child := filepath.Join(abs, filepath.FromSlash(e.path))
		if _, err := os.Lstat(child); os.IsNotExist(err) {
			past = append(past, e.path) // never delivered: a commit -a records its removal
			continue
		}
		if _, err := r.gitDir(ctx, child); err != nil {
			continue // delivered files with no checkout: nothing to rewind
		}
		if !r.hasCommit(ctx, child, e.sha) {
			past = append(past, e.path)
		} else if a := r.childTarget(ctx, child, e.sha); a.ok && r.pastChild(ctx, child, e.sha, a) {
			past = append(past, e.path)
		}
	}
	return strings.Join(past, ", ")
}

// pastChild reports a gitlink sha that some ending of the child's turn is
// neither at nor past: recording it leaves the child behind.
func (r *gitStateRun) pastChild(ctx context.Context, child, sha string, a childAnswer) bool {
	return slices.ContainsFunc(a.ends, func(end string) bool { return end != sha && !r.strictDescendant(ctx, child, sha, end) })
}

// lacking groups "<path> lacks <sha>" notes per path, so a child missing
// many commits reads as one item.
func lacking(unknown []string) string {
	var paths []string
	shas := map[string][]string{}
	for _, note := range unknown {
		path, sha, _ := strings.Cut(note, " lacks ")
		if _, seen := shas[path]; !seen {
			paths = append(paths, path)
		}
		shas[path] = append(shas[path], sha)
	}
	var out []string
	for _, path := range paths {
		if n := len(shas[path]); n > 1 {
			out = append(out, fmt.Sprintf("%s lacks %d commits (%s, ...)", path, n, shas[path][0]))
			continue
		}
		out = append(out, path+" lacks "+shas[path][0])
	}
	return strings.Join(out, ", ")
}

// tieEvidence is what the children say about contenders that tie on
// content.
type tieEvidence struct {
	pool    []string // the top contenders, in order, HEAD among them when it is
	decided int      // differing gitlinks whose child is exactly at one of them
	best    int      // the exact placements each of the pool got
	split   bool     // the evidence told the contenders apart
	behind  []string // the paths whose child has not reached a candidate's gitlink
	unknown []string // "<path> lacks <sha>": commits a child could not compare with
	fetched []string // "<path>: fetched before judging" (or its failure)
}

// childrenEvidence asks each child whose gitlink differs between the
// contenders where it ends once each contender is recorded (childTarget:
// its own turn with that gitlink) and judges the contender by it. A
// contender whose commit the child does not end at or past would move the
// parent past a bump the child never takes, and a later commit -a would
// record it reverted; the same holds for a submodule it adds whose checkout
// was never delivered, and for a commit the child lacks while its files, or
// a run that leaves it alone, hold it elsewhere. With withHead, tied[0] is
// HEAD, which such a record never drops: the others are dropped, and with
// none left HEAD alone is the pool.
// Without it (the parent's own content needs a move) they are only a last
// resort. The rest are scored by how many gitlinks name exactly where their
// child is.
func (r *gitStateRun) childrenEvidence(ctx context.Context, abs string, tied []string, withHead bool) tieEvidence {
	links := make([]map[string]string, len(tied))
	varying := map[string]bool{}
	for i, cand := range tied {
		entries, err := r.childGitlinks(ctx, abs, cand)
		if err != nil {
			return tieEvidence{pool: tied}
		}
		links[i] = map[string]string{}
		for _, e := range entries {
			links[i][e.path] = e.sha
		}
	}
	for i := range tied {
		for path, sha := range links[i] {
			for _, other := range links {
				if other[path] != sha {
					varying[path] = true
				}
			}
		}
	}
	var ev tieEvidence
	scores := make([]int, len(tied))
	behind := make([]bool, len(tied))
	for _, path := range slices.Sorted(maps.Keys(varying)) {
		child := filepath.Join(abs, filepath.FromSlash(path))
		if _, err := os.Lstat(child); os.IsNotExist(err) {
			// Not delivered: a contender that adds the submodule would leave
			// it deleted in the worktree.
			for i := range tied {
				if links[i][path] != "" && (!withHead || links[0][path] == "") {
					behind[i] = true
					if !slices.Contains(ev.behind, path) {
						ev.behind = append(ev.behind, path)
					}
				}
			}
			continue
		}
		dropped := func(i int, held string) {
			behind[i] = true
			label := path
			if held != "" {
				label += " (left alone: " + held + ")"
			}
			if !slices.Contains(ev.behind, label) {
				ev.behind = append(ev.behind, label)
			}
		}
		for i := range tied {
			if sha := links[i][path]; sha != "" && !r.hasCommit(ctx, child, sha) {
				r.fetchToJudge(ctx, child) // before any contender is judged
				break
			}
		}
		if outcome, ok := r.fetched[child]; ok {
			ev.fetched = append(ev.fetched, path+": "+outcome)
		}
		placed := false
		for i := range tied {
			sha := links[i][path]
			if sha == "" {
				continue // a removal: the checkout stays as an untracked directory
			}
			if !r.hasCommit(ctx, child, sha) {
				// It could be where the content is; only an exact match
				// elsewhere, or a child left alone, says it is not (the #179
				// case moves on).
				a := r.childTarget(ctx, child, "")
				if !a.ok {
					continue
				}
				if note := path + " lacks " + shortRev(sha); !slices.Contains(ev.unknown, note) {
					ev.unknown = append(ev.unknown, note)
				}
				if a.known || a.held != "" {
					dropped(i, a.held)
				}
				continue
			}
			// Where the child ends once this contender is recorded: its own
			// turn, with that gitlink.
			a := r.childTarget(ctx, child, sha)
			switch {
			case !a.ok:
				// No checkout to ask (a delivered addition has no .git).
			case r.pastChild(ctx, child, sha, a):
				dropped(i, a.held)
			case a.exact && len(a.ends) == 1 && a.ends[0] == sha:
				scores[i]++
				placed = true
			default:
				// The child ends at or ahead of it: a forward change to record.
			}
		}
		if placed {
			ev.decided++
		}
	}
	first := 0
	if withHead {
		first = 1
	}
	eligible := func(i int) bool { return i < first || !behind[i] }
	if !withHead && !slices.ContainsFunc(tied, func(c string) bool { return !behind[slices.Index(tied, c)] }) {
		// The parent's own content needs a move: the rules below pick among
		// them all, and breakTie warns about the children the move passes.
		eligible = func(int) bool { return true }
	}
	best, worst, dropped := -1, -1, false
	for i := range tied {
		if !eligible(i) {
			dropped = true
			continue
		}
		best = max(best, scores[i])
		if worst < 0 || scores[i] < worst {
			worst = scores[i]
		}
	}
	for i := range tied {
		if eligible(i) && scores[i] == best {
			ev.pool = append(ev.pool, tied[i])
		}
	}
	ev.best = best
	ev.split = best != worst || dropped
	return ev
}

// childTarget is where the child checkout at abs can end when its parent
// records gitlink ("" asks where it is without one): the child's own turn
// (classify, then a planned rescue) run on a report that is thrown away, so
// the parent's question and the child's answer cannot differ (#189 round
// 17). A rescue can fail after the parent has moved (a push, a ref it
// cannot create), so it has two endings, HEAD and its target, and the
// parent must hold for both (round 19). A child this run leaves alone (a
// lock, an operation, staged changes, a linked worktree, outside the
// restriction) stays at HEAD whatever its files show, and held says why.
func (r *gitStateRun) childTarget(ctx context.Context, abs, gitlink string) childAnswer {
	key := abs + "\x00" + gitlink
	if a, ok := r.targets[key]; ok {
		return a
	}
	fetches := len(r.fetched)
	a := r.childTurn(ctx, abs, gitlink)
	if len(r.fetched) != fetches {
		// A fetch during this answer may have changed what it rests on;
		// the repo being judged is judged again (judge), so do not keep it.
		return a
	}
	if r.targets == nil {
		r.targets = map[string]childAnswer{}
	}
	r.targets[key] = a
	return a
}

// childAnswer is a remembered childTarget. A child is asked only before its
// own turn moves anything, so an answer holds for the run.
type childAnswer struct {
	ends  []string // where it can end: one commit, or HEAD and a rescue's target
	exact bool     // its files match the one ending with no difference
	known bool     // its files match a commit it has (HEAD, where it moves, or a no-match child's rescue target) exactly
	ok    bool     // false when there is no checkout to read
	held  string   // why this run leaves it alone
}

func (r *gitStateRun) childTurn(ctx context.Context, abs, gitlink string) childAnswer {
	gitdir, err := r.gitDir(ctx, abs)
	if err != nil {
		return childAnswer{}
	}
	if held := r.leftAlone(ctx, abs, gitdir); held != "" {
		head, err := r.read(ctx, abs, "rev-parse", "--verify", "-q", "HEAD")
		if err != nil {
			return childAnswer{}
		}
		diffs, err := r.contentDiffs(ctx, abs, gitdir, head)
		if err != nil {
			return childAnswer{}
		}
		return childAnswer{ends: []string{head}, exact: diffs == 0, known: diffs == 0, ok: true, held: held}
	}
	rep := &GitRepoReport{}
	r.classify(ctx, abs, gitdir, gitlink, rep)
	if r.opts.Rescue && rep.Status == GitRepoNoMatch && rep.RescueTarget != "" {
		if r.planRescue(ctx, abs, rep); rep.Status == GitRepoRealignable {
			return childAnswer{ends: []string{rep.Head, rep.Target}, known: rep.TargetDiffs == 0, ok: true}
		}
	}
	switch {
	case rep.Head == "":
		return childAnswer{}
	case rep.Status == GitRepoRealignable:
		return childAnswer{ends: []string{rep.Target}, exact: rep.TargetDiffs == 0, known: rep.TargetDiffs == 0 || rep.HeadDiffs == 0, ok: true}
	}
	// A no-match child whose files match its rescue target exactly is at a
	// commit it has, with or without --rescue (#201).
	known := rep.HeadDiffs == 0 || rep.RescueTarget != "" && rep.rescueDiffs == 0
	return childAnswer{ends: []string{rep.Head}, exact: rep.HeadDiffs == 0, known: known, ok: true}
}

// leftAlone says why process will not realign the checkout at abs, as it
// decides: a linked worktree, a repo outside the restriction, a block.
func (r *gitStateRun) leftAlone(ctx context.Context, abs, gitdir string) string {
	if _, err := os.Stat(filepath.Join(gitdir, "commondir")); err == nil {
		return "linked worktree"
	}
	if rel, err := filepath.Rel(r.root, abs); err == nil && !r.included(filepath.ToSlash(rel)) {
		return "not named in the restriction"
	}
	return r.blockReason(ctx, abs, gitdir)
}

// sameOwnContent reports two commits with the same content outside their
// gitlinks and .gitmodules: they differ in their children only.
func (r *gitStateRun) sameOwnContent(ctx context.Context, abs, a, b string) bool {
	key := abs + "\x00" + a + "\x00" + b
	if same, ok := r.same[key]; ok {
		return same
	}
	code, err := r.run(ctx, abs, nil, true, "diff", "--quiet", "--ignore-submodules", a, b, "--", ".", ":(exclude).gitmodules")
	if err != nil || code > 1 {
		return false
	}
	if r.same == nil {
		r.same = map[string]bool{}
	}
	if isCommitID(a) && isCommitID(b) {
		r.same[key] = code == 0
	}
	return code == 0
}

// forgetFetched drops what a fetch in the repo at abs can change: the
// commits it lacked, and every remembered childTarget answer (its
// upstream chain moved, and answers about its parents rest on it).
func (r *gitStateRun) forgetFetched(abs string) {
	for key := range r.absent {
		if strings.HasPrefix(key, abs+"\x00") {
			delete(r.absent, key)
		}
	}
	r.targets = nil
}

// fetchToJudge fetches a child that lacks a commit a candidate records,
// once per run and only under --apply --fetch, so it is judged with the
// commit present (#201). It fetches from the child's origin as it is: a
// moved URL is re-pointed in the child's own turn (missingGitlink). A
// child the run leaves alone, or one with no checkout of its own, is not
// fetched.
func (r *gitStateRun) fetchToJudge(ctx context.Context, child string) {
	if !r.opts.Apply || !r.opts.Fetch {
		return
	}
	if _, done := r.fetched[child]; done {
		return
	}
	gitdir, err := r.gitDir(ctx, child)
	if err != nil || r.leftAlone(ctx, child, gitdir) != "" {
		return
	}
	from, _ := r.read(ctx, child, "remote", "get-url", "origin")
	outcome := fetchedToJudge
	if err := r.fetchOrigin(ctx, child); err != nil {
		outcome = "fetch failed before judging: " + shortErr(err)
	}
	if r.fetched == nil {
		r.fetched, r.fetchedFrom = map[string]string{}, map[string]string{}
	}
	r.fetched[child], r.fetchedFrom[child] = outcome, from
}

// fetchedToJudge is fetchToJudge's outcome when the fetch succeeded.
const fetchedToJudge = "fetched before judging"

// isCommitID reports a full object id, the only key the run's caches take.
func isCommitID(s string) bool {
	return (len(s) == 40 || len(s) == 64) && strings.Trim(s, "0123456789abcdef") == ""
}

func (r *gitStateRun) hasCommit(ctx context.Context, abs, sha string) bool {
	key := abs + "\x00" + sha
	if r.present[key] {
		return true
	}
	if r.absent[key] {
		return false
	}
	if _, err := r.read(ctx, abs, "cat-file", "-e", sha+"^{commit}"); err != nil {
		if isCommitID(sha) {
			if r.absent == nil {
				r.absent = map[string]bool{}
			}
			r.absent[key] = true
		}
		return false
	}
	if r.present == nil {
		r.present = map[string]bool{}
	}
	if isCommitID(sha) {
		r.present[key] = true
	}
	return true
}

const (
	staleRebaseHead = "stale REBASE_HEAD"
	gitlinkMissing  = "parent gitlink object is not present locally"
)

// blockReason reports why a repo must be skipped and reported rather than
// classified or realigned: a lock, an operation in progress, unmerged
// entries or staged changes (the 09-24 stale-lock incident class).
func (r *gitStateRun) blockReason(ctx context.Context, abs, gitdir string) string {
	if _, err := os.Stat(filepath.Join(gitdir, "index.lock")); err == nil {
		return "index.lock present"
	}
	// Unmerged before the operation markers: a real merge or cherry-pick
	// conflict sets both, and the unmerged entries are the actionable part.
	if out, err := r.read(ctx, abs, "ls-files", "-u"); err != nil {
		return "cannot inspect index entries: " + shortErr(err)
	} else if strings.TrimSpace(out) != "" {
		return "unmerged index entries"
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		if _, err := os.Stat(filepath.Join(gitdir, marker)); err == nil {
			return "operation in progress (" + marker + ")"
		}
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if st, err := os.Stat(filepath.Join(gitdir, dir)); err == nil && st.IsDir() {
			return "operation in progress (" + dir + ")"
		}
	}
	// A rebase in progress always has one of the directories above; a
	// REBASE_HEAD without them is a leftover (#178).
	if _, err := os.Stat(filepath.Join(gitdir, "REBASE_HEAD")); err == nil {
		return staleRebaseHead
	}
	if _, err := r.read(ctx, abs, "diff", "--cached", "--quiet"); err != nil {
		var exitErr *gitExitError
		if errors.As(err, &exitErr) && exitErr.code == 1 {
			return "staged changes"
		}
		return "cannot inspect staged state: " + shortErr(err)
	}
	return ""
}

// candidates returns the strict-descendant candidate commits, parent gitlink
// first, then the first-parent chain from HEAD to the branch upstream
// (nearest HEAD first). The command never fetches; the chain reflects
// whatever the caller fetched beforehand.
func (r *gitStateRun) candidates(ctx context.Context, abs, head, gitlink string) ([]string, error) {
	var out []string
	if gitlink != "" && gitlink != head {
		if !r.hasCommit(ctx, abs, gitlink) {
			return nil, errors.New(gitlinkMissing)
		}
		if r.strictDescendant(ctx, abs, head, gitlink) {
			out = append(out, gitlink)
		}
	}
	if _, err := r.read(ctx, abs, "rev-parse", "--verify", "-q", "@{upstream}"); err != nil {
		return r.withCandidateRefs(ctx, abs, head, out) // no upstream configured
	}
	chain, err := r.read(ctx, abs, "rev-list", "--first-parent", "HEAD..@{upstream}")
	if err != nil {
		return nil, errors.New("cannot enumerate the upstream first-parent chain: " + shortErr(err))
	}
	lines := strings.Split(strings.TrimSpace(chain), "\n")
	for i := len(lines) - 1; i >= 0; i-- { // rev-list is newest-first; want nearest HEAD first
		sha := strings.TrimSpace(lines[i])
		if sha == "" || sha == head || sha == gitlink {
			continue // the parent gitlink is already the first candidate
		}
		if r.strictDescendant(ctx, abs, head, sha) {
			out = append(out, sha)
		}
	}
	return r.withCandidateRefs(ctx, abs, head, out)
}

// withCandidateRefs appends the commits the --candidate-refs patterns name
// (an annotated tag's commit), strict descendants of HEAD that are not
// candidates already, and remembers the ref each came from (#217). A
// pattern that matches nothing adds nothing; one git cannot read is an
// error.
func (r *gitStateRun) withCandidateRefs(ctx context.Context, abs, head string, out []string) ([]string, error) {
	if len(r.opts.CandidateRefs) == 0 {
		return out, nil
	}
	list, err := r.read(ctx, abs, append([]string{"for-each-ref", "--format=%(refname)%00%(objectname)%00%(*objectname)", "--"}, r.opts.CandidateRefs...)...)
	if err != nil {
		return out, errors.New("cannot read --candidate-refs: " + shortErr(err))
	}
	own := len(out) // the gitlink and upstream candidates
	for _, line := range strings.Split(list, "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 3 {
			continue
		}
		sha := f[1]
		if f[2] != "" {
			sha = f[2]
		}
		key := abs + "\x00" + sha
		if slices.Contains(out[:own], sha) {
			delete(r.candidateRefs, key) // offered by the gitlink or upstream too
			continue
		}
		if slices.Contains(out[own:], sha) || !r.strictDescendant(ctx, abs, head, sha) {
			continue
		}
		out = append(out, sha)
		if r.candidateRefs == nil {
			r.candidateRefs = map[string]string{}
		}
		r.candidateRefs[key] = f[0]
	}
	return out, nil
}

// strictDescendant reports whether cand is a descendant of head and not head
// itself. This is the fast-forward-only guarantee: realign never moves a ref
// backward or sideways.
func (r *gitStateRun) strictDescendant(ctx context.Context, abs, head, cand string) bool {
	if cand == head {
		return false
	}
	key := abs + "\x00" + head + "\x00" + cand
	if yes, ok := r.ancestry[key]; ok {
		return yes
	}
	code, err := r.run(ctx, abs, nil, true, "merge-base", "--is-ancestor", head, cand)
	if err != nil || code > 1 {
		return false // a missing commit: ask again after a fetch
	}
	if r.ancestry == nil {
		r.ancestry = map[string]bool{}
	}
	if isCommitID(head) && isCommitID(cand) {
		r.ancestry[key] = code == 0
	}
	return code == 0
}

// contentDiffs counts tracked files whose worktree content differs from a
// commit's tree, using a throwaway index: copy the real index to a temp file,
// read-tree -m into it, refresh stat info, then diff-files. This writes no
// objects and holds no lock on the real index. Untracked files never appear.
func (r *gitStateRun) contentDiffs(ctx context.Context, abs, gitdir, commit string) (int, error) {
	key := abs + "\x00" + commit
	if n, ok := r.diffs[key]; ok {
		return n, nil
	}
	tempIndex, cleanup, err := r.tempIndexFor(ctx, abs, gitdir, commit)
	if err != nil {
		return -1, err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + tempIndex}
	// update-index exits non-zero exactly when files need update; that is the
	// expected signal here, not an error.
	_, _ = r.run(ctx, abs, env, false, "update-index", "-q", "--refresh")
	// Gitlinks are the children's business: a child realigns separately and
	// its HEAD is stale until then, so counting it here would favor the
	// parent commit that changed the fewest gitlinks (#177). .gitmodules is
	// never carried by peer sync, so it is no evidence either (#179).
	out, err := r.readEnv(ctx, abs, env, "diff-files", "--ignore-submodules", "--name-only", "--", ":(exclude).gitmodules")
	if err != nil {
		return -1, err
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	if r.diffs == nil {
		r.diffs = map[string]int{}
	}
	if isCommitID(commit) { // a ref name can move within a run
		r.diffs[key] = count
	}
	return count, nil
}

// tempIndexFor builds a temp index holding the candidate tree. It first
// tries seeding from the repo's real index so unchanged entries keep their
// stat info; a one-tree read-tree -m refuses to replace entries the worktree
// has modified ("not uptodate"), and that is exactly the realign case, so a
// refusal retries from an empty index and lets the refresh decide by content.
// The caller removes the file via cleanup.
func (r *gitStateRun) tempIndexFor(ctx context.Context, abs, gitdir, commit string) (string, func(), error) {
	seed, err := os.ReadFile(filepath.Join(gitdir, "index"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", func() {}, err
	}
	tempIndex, cleanup, err := r.writeTempIndex(ctx, abs, commit, seed)
	if err == nil {
		return tempIndex, cleanup, nil
	}
	cleanup()
	return r.writeTempIndex(ctx, abs, commit, nil)
}

func (r *gitStateRun) writeTempIndex(ctx context.Context, abs, commit string, seed []byte) (string, func(), error) {
	tmp, err := os.CreateTemp("", "dot-gitstate-index-*")
	if err != nil {
		return "", func() {}, err
	}
	tempIndex := tmp.Name()
	cleanup := func() { _ = os.Remove(tempIndex) }
	if _, err := tmp.Write(seed); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if len(seed) == 0 {
		// A zero-byte index file is corrupt to git; an empty index is a
		// MISSING file. read-tree creates it at the given path.
		if err := os.Remove(tempIndex); err != nil {
			return "", func() {}, err
		}
	}
	env := []string{"GIT_INDEX_FILE=" + tempIndex}
	// read-tree is a write to the temp index only; its lock is the temp's own.
	if _, err := r.runOutput(ctx, abs, env, false, "read-tree", "-m", commit); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tempIndex, cleanup, nil
}

// realign moves HEAD and the index to rep.Target through git's lockfile
// protocol: create index.lock O_EXCL, write the temp index into it,
// compare-and-swap HEAD from the recorded old value, rename the lock over the
// index. The worktree is never written by git. On a compare-and-swap failure
// the lock is removed and the repo is left exactly as it was.
func (r *gitStateRun) realign(ctx context.Context, abs, gitdir string, rep *GitRepoReport) {
	unresolvable := func(reason string) {
		rep.Status = GitRepoUnresolvable
		rep.Reason = reason
	}
	skipped := func(reason string) {
		rep.Status = GitRepoSkipped
		rep.Reason = reason
	}

	tempIndex, cleanup, err := r.tempIndexFor(ctx, abs, gitdir, rep.Target)
	if err != nil {
		unresolvable("cannot build target index: " + shortErr(err))
		return
	}
	defer cleanup()

	index := filepath.Join(gitdir, "index")
	lockPath := index + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		skipped("index.lock appeared before realign: " + shortErr(err))
		return
	}
	built, err := os.ReadFile(tempIndex)
	if err == nil {
		_, err = lock.Write(built)
	}
	if closeErr := lock.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(lockPath)
		unresolvable("cannot write index lock: " + shortErr(err))
		return
	}

	if _, err := r.runOutput(ctx, abs, nil, false, "update-ref", "-m", "dot peer realign", "HEAD", rep.Target, rep.Head); err != nil {
		_ = os.Remove(lockPath)
		skipped("compare-and-swap failed; repo untouched: " + shortErr(err))
		return
	}
	if err := os.Rename(lockPath, index); err != nil {
		// HEAD already moved; roll it back so the repo is left exactly as
		// found, and never report a realign that did not complete.
		if _, rbErr := r.runOutput(ctx, abs, nil, false, "update-ref", "-m", "dot peer realign rollback", "HEAD", rep.Head); rbErr != nil {
			rep.PreviousHead = rep.Head
			rep.Status = GitRepoUnresolvable
			rep.Reason = "index rename failed and HEAD rollback failed (HEAD is " + rep.Target + "; recover manually with git update-ref HEAD " + rep.Head + "): " + shortErr(err)
			return
		}
		_ = os.Remove(lockPath)
		unresolvable("index rename failed after HEAD moved; HEAD rolled back: " + shortErr(err))
		return
	}
	rep.PreviousHead = rep.Head
	rep.Status = GitRepoRealigned
	rep.Undo = "git -C " + shellWord(abs) + " reset --mixed -q " + rep.Head
}

// gitDir resolves the absolute gitdir of the checkout at abs.
//
// abs must be the top level of that checkout. git searches upward from a
// directory without its own .git, so an uninitialized submodule (an empty
// directory, a deinit, files peer sync delivered without the gitfile) would
// otherwise resolve to the parent: the parent would be classified, fetched
// and re-pointed as if it were the child, and ls-tree there lists the
// child's own gitlink as "./", recursing forever.
func (r *gitStateRun) gitDir(ctx context.Context, abs string) (string, error) {
	top, err := r.read(ctx, abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	want, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	// SameFile, not a path compare: symlinks and a case-insensitive volume
	// spell one directory several ways.
	if got, err := os.Stat(top); err != nil || !os.SameFile(got, want) {
		return "", fmt.Errorf("%s is not the top of a checkout (git resolves it to %s)", abs, top)
	}
	return r.read(ctx, abs, "rev-parse", "--absolute-git-dir")
}

type gitlinkEntry struct {
	path string // relative to the repo root
	sha  string
}

// childGitlinks lists the submodule gitlinks recorded in commit rev: ls-tree
// mode 160000 entries, at any depth within this repo (ls-tree does not cross
// into submodules).
func (r *gitStateRun) childGitlinks(ctx context.Context, abs, rev string) ([]gitlinkEntry, error) {
	key := abs + "\x00" + rev
	if entries, ok := r.links[key]; ok {
		return entries, nil
	}
	out, err := r.read(ctx, abs, "ls-tree", "-r", "-z", rev)
	if err != nil {
		return nil, err
	}
	var entries []gitlinkEntry
	for _, record := range bytes.Split([]byte(out), []byte{0}) {
		meta, path, ok := bytes.Cut(record, []byte("\t"))
		if !ok {
			continue
		}
		fields := bytes.Fields(meta)
		if len(fields) != 3 || string(fields[0]) != "160000" {
			continue
		}
		entries = append(entries, gitlinkEntry{path: string(path), sha: string(fields[2])})
	}
	if isCommitID(rev) { // a commit's tree never changes; HEAD does
		if r.links == nil {
			r.links = map[string][]gitlinkEntry{}
		}
		r.links[key] = entries
	}
	return entries, nil
}

// read runs a read-only git command and returns trimmed stdout. Read
// commands always carry --no-optional-locks so classification never takes an
// optional index lock (the temp-index refresh writes only the temp file).
func (r *gitStateRun) read(ctx context.Context, abs string, args ...string) (string, error) {
	return r.readEnv(ctx, abs, nil, args...)
}

func (r *gitStateRun) readEnv(ctx context.Context, abs string, env []string, args ...string) (string, error) {
	out, err := r.runOutput(ctx, abs, env, true, args...)
	return strings.TrimSpace(out), err
}

// run executes git and reports the exit code. A non-zero git exit is an
// answer, not an error (diff --quiet, merge-base --is-ancestor); only a
// command that could not run at all returns a non-nil error.
func (r *gitStateRun) run(ctx context.Context, abs string, env []string, readOnly bool, args ...string) (int, error) {
	_, err := r.runOutput(ctx, abs, env, readOnly, args...)
	if err == nil {
		return 0, nil
	}
	var exitErr *gitExitError
	if errors.As(err, &exitErr) {
		return exitErr.code, nil
	}
	return -1, err
}

func (r *gitStateRun) runOutput(ctx context.Context, abs string, env []string, readOnly bool, args ...string) (string, error) {
	full := append(r.noHooks(ctx, abs), "-C", abs)
	if readOnly {
		full = append([]string{"--no-optional-locks"}, full...)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, r.git, full...)
	cmd.Dir = abs
	// After a timeout kills git, its ssh or https helper can hold the output
	// pipes open; stop waiting on them.
	cmd.WaitDelay = 5 * time.Second
	base := r.env
	if base == nil {
		base = os.Environ() // a run built without runGitState
	}
	cmd.Env = append(append(append([]string{}, base...), env...), hookOffEnv+"=false")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return stdout.String(), &gitExitError{code: exitErr.ExitCode(), stderr: strings.TrimSpace(stderr.String())}
		}
		return stdout.String(), err
	}
	return stdout.String(), nil
}

// noRepoHooks keeps every git command a peer git run starts from running a
// hook of the repo it works in, a preview included (#204): the hooks
// directory (core.hooksPath), hooks defined in config (git 2.55+,
// hook.<event>, for each event these commands fire; noHooks adds each
// configured hook by name for git 2.54), and the fsmonitor hook.
var noRepoHooks = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "hook.post-checkout.enabled=false",
	"-c", "hook.post-index-change.enabled=false",
	"-c", "hook.reference-transaction.enabled=false",
	"-c", "hook.pre-push.enabled=false",
	"-c", "hook.pre-auto-gc.enabled=false",
}

// noHooks is noRepoHooks plus hook.<name>.enabled=false for every hook the
// repo's config defines: git 2.54 runs config hooks but reads
// hook.<event>.enabled only as a hook named after the event, so each hook is
// turned off by its own name. The names are read NUL-separated and passed
// with --config-env (split at the last "="), so a name may hold spaces or
// "=". The names are read once per repo.
func (r *gitStateRun) noHooks(ctx context.Context, abs string) []string {
	names, ok := r.hookNames[abs]
	if !ok {
		cmd := exec.CommandContext(ctx, r.git, "-C", abs, "config", "-z", "--name-only", "--get-regexp", `^hook\..*\.event$`)
		cmd.Env = r.env        // nil inherits the process environment
		out, _ := cmd.Output() // exit 1: no hook configured
		for _, key := range strings.Split(string(out), "\x00") {
			if key != "" {
				names = append(names, "--config-env=hook."+strings.TrimSuffix(strings.TrimPrefix(key, "hook."), ".event")+".enabled="+hookOffEnv)
			}
		}
		if r.hookNames == nil {
			r.hookNames = map[string][]string{}
		}
		r.hookNames[abs] = names
	}
	return append(slices.Clone(noRepoHooks), names...)
}

// hookOffEnv holds "false" for noHooks' --config-env flags.
const hookOffEnv = "DOT_PEER_GIT_HOOK_OFF"

// gitExitError carries git's exit code and stderr without the "exit status N"
// wrapper text, so skip/unresolvable reasons stay readable.
type gitExitError struct {
	code   int
	stderr string
}

func (e *gitExitError) Error() string {
	if e.stderr == "" {
		return "git exit status " + strconv.Itoa(e.code)
	}
	return e.stderr
}

const maxReasonLen = 120

func shortErr(err error) string {
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > maxReasonLen {
		msg = msg[:maxReasonLen] + "..."
	}
	return msg
}
